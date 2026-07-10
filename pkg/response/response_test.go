package response

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
)

func TestErrorMapsClientInputErrorToBadRequest(t *testing.T) {
	_, err := strconv.ParseUint("bad-id", 10, 32)
	if err == nil {
		t.Fatal("ParseUint returned nil error")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	Error(ctx, err)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	var body Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if body.Code != string(apperrors.ErrCodeInvalidInput) {
		t.Fatalf("code = %q, want %q", body.Code, apperrors.ErrCodeInvalidInput)
	}
}

func TestErrorMapsUnknownJSONFieldToBadRequest(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	Error(ctx, errors.New(`json: unknown field "is_healthy"`))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	var body Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if body.Code != string(apperrors.ErrCodeInvalidInput) {
		t.Fatalf("code = %q, want %q", body.Code, apperrors.ErrCodeInvalidInput)
	}
	if !strings.Contains(body.Detail, "unknown field") {
		t.Fatalf("detail = %q, want unknown field context", body.Detail)
	}
}

func TestErrorMapsUnknownErrorToInternal(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	Error(ctx, errors.New("unexpected secret detail"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	var body Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if body.Detail != "" {
		t.Fatalf("detail = %q, want empty for internal errors", body.Detail)
	}
}

func TestErrorHidesInternalAppErrorDetail(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	Error(ctx, apperrors.NewInternal("internal server error", errors.New("database down")).
		WithDetail(`user_token="secret-token" callback=https://example.com/hook?token=secret`))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	var body Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if body.Detail != "" {
		t.Fatalf("detail = %q, want empty for internal app errors", body.Detail)
	}
}

func TestErrorRedactsClientAppErrorDetail(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	Error(ctx, apperrors.NewInvalidInput("invalid callback").
		WithDetail(`callback_url=https://user:pass@example.com/hook?token=secret#frag user_token="secret-token"`))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	var body Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(body.Detail, forbidden) {
			t.Fatalf("detail exposed %q: %s", forbidden, body.Detail)
		}
	}
	if !strings.Contains(body.Detail, `user_token="<redacted>"`) || !strings.Contains(body.Detail, "https://example.com/hook") {
		t.Fatalf("detail did not keep useful redacted context: %s", body.Detail)
	}
}

func TestErrorRedactsClientAppErrorMessage(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	Error(ctx, apperrors.NewInvalidInput(
		`invalid user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`,
	))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	var body Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(body.Message, forbidden) {
			t.Fatalf("message exposed %q: %s", forbidden, body.Message)
		}
	}
	if !strings.Contains(body.Message, `user_token="<redacted>"`) || !strings.Contains(body.Message, "https://example.com/hook") {
		t.Fatalf("message did not keep useful redacted context: %s", body.Message)
	}
}

func TestErrorMapsAccountAlreadyExistsToConflict(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	Error(ctx, apperrors.NewAccountAlreadyExists("guild-1", "channel-1"))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
}

func TestErrorMapsTaskAlreadyTerminalToConflict(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	Error(ctx, apperrors.NewTaskAlreadyTerminal("task-1", "TIMEOUT"))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
}
