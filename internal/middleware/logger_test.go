package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/pkg/response"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestSanitizeQueryRedactsSensitiveValues(t *testing.T) {
	query := sanitizeQuery("limit=10&user_token=secret-token&OSSAccessKeyId=secret-key&Signature=sig&password=pw&prompt=cat")

	if strings.Contains(query, "secret-token") ||
		strings.Contains(query, "secret-key") ||
		strings.Contains(query, "Signature=sig") ||
		strings.Contains(query, "password=pw") {
		t.Fatalf("query leaked sensitive value: %s", query)
	}
	if !strings.Contains(query, "limit=10") {
		t.Fatalf("query should preserve non-sensitive limit: %s", query)
	}
	if !strings.Contains(query, "prompt=cat") {
		t.Fatalf("query should preserve non-sensitive prompt: %s", query)
	}
	if !strings.Contains(query, "user_token=%2A%2A%2Aredacted%2A%2A%2A") {
		t.Fatalf("query did not redact user_token: %s", query)
	}
}

func TestSanitizeQueryRedactsNestedURLSecrets(t *testing.T) {
	query := sanitizeQuery("callback_url=https%3A%2F%2Fcb.example%2Fhook%3Ftoken%3Dsecret%23frag&image_url=https%3A%2F%2Fcdn.example%2Fimg.png%3FSignature%3Dsig&prompt=cat")

	if strings.Contains(query, "secret") ||
		strings.Contains(query, "token%3D") ||
		strings.Contains(query, "Signature") ||
		strings.Contains(query, "frag") {
		t.Fatalf("query leaked nested URL secret: %s", query)
	}
	if !strings.Contains(query, "prompt=cat") {
		t.Fatalf("query should preserve non-sensitive prompt: %s", query)
	}

	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("failed to parse sanitized query: %v", err)
	}
	if got, want := values.Get("callback_url"), "https://cb.example/hook"; got != want {
		t.Fatalf("callback_url = %q, want %q", got, want)
	}
	if got, want := values.Get("image_url"), "https://cdn.example/img.png"; got != want {
		t.Fatalf("image_url = %q, want %q", got, want)
	}
}

func TestSanitizeQueryRedactsURLSecretsInGenericValues(t *testing.T) {
	query := sanitizeQuery("callback=https%3A%2F%2Fcb.example%2Fhook%3Ftoken%3Dsecret%23frag&prompt=cat")

	if strings.Contains(query, "secret") ||
		strings.Contains(query, "token%3D") ||
		strings.Contains(query, "frag") {
		t.Fatalf("query leaked URL secret from generic value: %s", query)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("failed to parse sanitized query: %v", err)
	}
	if got, want := values.Get("callback"), "https://cb.example/hook"; got != want {
		t.Fatalf("callback = %q, want %q", got, want)
	}
	if got, want := values.Get("prompt"), "cat"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestSanitizeQueryRedactsSensitivePatternsInGenericValues(t *testing.T) {
	query := sanitizeQuery("debug=user_token%3Dsecret-token+custom_id%3Dsecret-button")

	for _, forbidden := range []string{"secret-token", "custom_id", "secret-button"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("query leaked %q from generic value: %s", forbidden, query)
		}
	}
	if !strings.Contains(query, "%3Credacted%3E") {
		t.Fatalf("query did not keep redaction marker: %s", query)
	}
}

func TestSanitizeQueryRedactsMalformedURLValuesSafely(t *testing.T) {
	query := sanitizeQuery("callback_url=https%3A%2F%2Fcb.example%2F%25zz%3Ftoken%3Dsecret")

	if strings.Contains(query, "secret") || strings.Contains(query, "token") {
		t.Fatalf("query leaked malformed URL secret: %s", query)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("failed to parse sanitized query: %v", err)
	}
	if got := values.Get("callback_url"); got != redactedLogValue {
		t.Fatalf("callback_url = %q, want redacted marker", got)
	}
}

func TestSanitizeQueryRedactsNonHTTPURLQuerySecrets(t *testing.T) {
	query := sanitizeQuery("callback_url=ftp%3A%2F%2Fcb.example%2Fhook%3Ftoken%3Dsecret%23frag")

	if strings.Contains(query, "secret") ||
		strings.Contains(query, "token%3D") ||
		strings.Contains(query, "frag") {
		t.Fatalf("query leaked non-http URL secret: %s", query)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("failed to parse sanitized query: %v", err)
	}
	if got, want := values.Get("callback_url"), redactedLogValue; got != want {
		t.Fatalf("callback_url = %q, want %q", got, want)
	}
}

func TestSanitizeQueryRedactsRelativeURLQuerySecrets(t *testing.T) {
	query := sanitizeQuery("callback_url=%2Fhook%3Ftoken%3Dsecret%23frag")

	if strings.Contains(query, "secret") ||
		strings.Contains(query, "token%3D") ||
		strings.Contains(query, "frag") {
		t.Fatalf("query leaked relative URL secret: %s", query)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("failed to parse sanitized query: %v", err)
	}
	if got, want := values.Get("callback_url"), redactedLogValue; got != want {
		t.Fatalf("callback_url = %q, want %q", got, want)
	}
}

func TestSanitizeQueryRedactsBareURLQueryValues(t *testing.T) {
	query := sanitizeQuery("callback_url=secret-token")

	if strings.Contains(query, "secret-token") {
		t.Fatalf("query leaked bare URL value: %s", query)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("failed to parse sanitized query: %v", err)
	}
	if got, want := values.Get("callback_url"), redactedLogValue; got != want {
		t.Fatalf("callback_url = %q, want %q", got, want)
	}
}

func TestSanitizeQueryHandlesMalformedQuerySafely(t *testing.T) {
	query := sanitizeQuery("%zzuser_token=secret-token")

	if query != redactedLogValue {
		t.Fatalf("query = %q, want redacted marker", query)
	}
}

func TestIsSensitiveQueryKey(t *testing.T) {
	for _, key := range []string{
		"user_token",
		"access-key-secret",
		"OSSAccessKeyId",
		"api_key",
		"Authorization",
		"password",
		"Signature",
	} {
		if !isSensitiveQueryKey(key) {
			t.Fatalf("key %q should be sensitive", key)
		}
	}

	if isSensitiveQueryKey("prompt") {
		t.Fatalf("prompt should not be sensitive")
	}
}

func TestMiddlewareAllowsNilLogger(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(Recovery(nil))
	router.Use(RequestLogger(nil))
	router.GET("/ok", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok?Signature=secret-signature", nil)

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestRequestBodyLimitRejectsOversizedJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(RequestBodyLimit(8))
	router.POST("/json", func(c *gin.Context) {
		var body map[string]string
		if err := c.ShouldBindJSON(&body); err != nil {
			response.Error(c, err)
			return
		}
		response.Success(c, body)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/json", bytes.NewBufferString(`{"prompt":"too large"}`))
	req.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	var body response.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if body.Code != "INVALID_INPUT" {
		t.Fatalf("code = %q, want INVALID_INPUT", body.Code)
	}
	if !strings.Contains(body.Detail, "request body too large") {
		t.Fatalf("detail = %q, want body too large context", body.Detail)
	}
}

func TestRequestLoggerLogsErrorCountInsteadOfErrorText(t *testing.T) {
	gin.SetMode(gin.TestMode)

	core, logs := observer.New(zap.WarnLevel)
	router := gin.New()
	router.Use(RequestLogger(zap.New(core)))
	router.GET("/bad", func(c *gin.Context) {
		_ = c.Error(errors.New("user_token=secret callback_url=https://cb.example/hook?token=secret"))
		c.Status(http.StatusBadRequest)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bad?user_token=secret-token", nil)

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if _, ok := fields["errors"]; ok {
		t.Fatalf("request log should not include raw error text: %#v", fields)
	}
	if got := fmt.Sprint(fields["error_count"]); got != "1" {
		t.Fatalf("error_count = %s, want 1", got)
	}
	if logText := fmt.Sprint(fields); strings.Contains(logText, "secret") || strings.Contains(logText, "callback_url") {
		t.Fatalf("request log leaked sensitive error text: %s", logText)
	}
}

func TestRequestLoggerRedactsUserControlledFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	core, logs := observer.New(zap.InfoLevel)
	router := gin.New()
	router.Use(RequestLogger(zap.New(core)))
	router.GET("/leak/:value", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/leak/user_token=secret-token", nil)
	req.Header.Set("User-Agent", `client user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`)

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	logText := fmt.Sprint(fields)
	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(logText, forbidden) {
			t.Fatalf("request log exposed %q: %s", forbidden, logText)
		}
	}
	if !strings.Contains(fmt.Sprint(fields["path"]), "user_token=<redacted>") {
		t.Fatalf("path was not redacted: %#v", fields["path"])
	}
	if !strings.Contains(fmt.Sprint(fields["user_agent"]), `user_token="<redacted>"`) ||
		!strings.Contains(fmt.Sprint(fields["user_agent"]), "https://example.com/hook") {
		t.Fatalf("user_agent was not usefully redacted: %#v", fields["user_agent"])
	}
}

func TestRecoveryReturnsStandardResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(Recovery(zap.NewNop()))
	router.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	var body response.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if body.Code != "INTERNAL_ERROR" {
		t.Fatalf("code = %q, want INTERNAL_ERROR", body.Code)
	}
	if body.Message != "internal server error" {
		t.Fatalf("message = %q, want internal server error", body.Message)
	}
	if body.Detail != "" {
		t.Fatalf("detail = %q, want empty", body.Detail)
	}
}

func TestRecoveryRedactsPanicLog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	core, logs := observer.New(zap.ErrorLevel)
	router := gin.New()
	router.Use(Recovery(zap.New(core)))
	router.GET("/panic/:value", func(c *gin.Context) {
		panic(`user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic/user_token=secret-token", nil)

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	entries := logs.FilterMessage("Panic recovered").All()
	if len(entries) != 1 {
		t.Fatalf("panic log entries = %d, want 1", len(entries))
	}
	errorValue, ok := entries[0].ContextMap()["error"].(string)
	if !ok {
		t.Fatalf("panic log error field type = %T, want string", entries[0].ContextMap()["error"])
	}
	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(errorValue, forbidden) {
			t.Fatalf("panic log exposed %q: %s", forbidden, errorValue)
		}
	}
	if !strings.Contains(errorValue, `user_token="<redacted>"`) || !strings.Contains(errorValue, "https://example.com/hook") {
		t.Fatalf("panic log did not keep useful redacted context: %s", errorValue)
	}
	pathValue, ok := entries[0].ContextMap()["path"].(string)
	if !ok {
		t.Fatalf("panic log path field type = %T, want string", entries[0].ContextMap()["path"])
	}
	if strings.Contains(pathValue, "secret-token") {
		t.Fatalf("panic log path exposed secret token: %s", pathValue)
	}
	if !strings.Contains(pathValue, "user_token=<redacted>") {
		t.Fatalf("panic log path was not redacted: %s", pathValue)
	}
}
