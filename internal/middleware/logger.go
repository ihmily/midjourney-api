package middleware

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/redact"
	"github.com/trae/midjourney-api/pkg/response"
	"go.uber.org/zap"
)

func RequestLogger(logger *zap.Logger) gin.HandlerFunc {
	logger = middlewareLogger(logger)

	return func(c *gin.Context) {
		requestID := uuid.New().String()
		c.Set("request_id", requestID)

		start := time.Now()
		path := redact.Text(c.Request.URL.Path)
		query := sanitizeQuery(c.Request.URL.RawQuery)
		userAgent := redact.Text(c.Request.UserAgent())

		c.Next()

		latency := time.Since(start)

		fields := []zap.Field{
			zap.String("request_id", requestID),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("query", query),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", latency),
			zap.String("client_ip", c.ClientIP()),
			zap.String("user_agent", userAgent),
		}

		if len(c.Errors) > 0 {
			fields = append(fields, zap.Int("error_count", len(c.Errors)))
		}

		if c.Writer.Status() >= 500 {
			logger.Error("HTTP Request", fields...)
		} else if c.Writer.Status() >= 400 {
			logger.Warn("HTTP Request", fields...)
		} else {
			logger.Info("HTTP Request", fields...)
		}
	}
}

const redactedLogValue = "***redacted***"

func sanitizeQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return redactedLogValue
	}

	for key := range values {
		if isSensitiveQueryKey(key) {
			for i := range values[key] {
				values[key][i] = redactedLogValue
			}
			continue
		}

		if isURLQueryKey(key) {
			for i, value := range values[key] {
				values[key][i] = sanitizeQueryURLValue(value)
			}
			continue
		}

		for i, value := range values[key] {
			values[key][i] = sanitizeQueryValue(value)
		}
	}

	return values.Encode()
}

func isSensitiveQueryKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "authorization") ||
		strings.Contains(normalized, "access_key") ||
		strings.Contains(normalized, "accesskey") ||
		strings.Contains(normalized, "api_key") ||
		strings.Contains(normalized, "signature")
}

func isURLQueryKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	return normalized == "url" ||
		strings.HasSuffix(normalized, "_url") ||
		strings.HasSuffix(normalized, "url")
}

func sanitizeQueryURLValue(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return redactedLogValue
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return redactedLogValue
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return redactedLogValue
	}
	return redact.URL(value)
}

func sanitizeQueryValue(value string) string {
	return redact.Text(value)
}

func Recovery(logger *zap.Logger) gin.HandlerFunc {
	logger = middlewareLogger(logger)

	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				requestID, _ := c.Get("request_id")
				requestIDStr, _ := requestID.(string)
				logger.Error("Panic recovered",
					zap.String("error", redact.Text(fmt.Sprint(err))),
					zap.String("request_id", requestIDStr),
					zap.String("path", redact.Text(c.Request.URL.Path)),
					zap.Stack("stack"),
				)

				c.JSON(http.StatusInternalServerError, response.Response{
					Code:    string(apperrors.ErrCodeInternal),
					Message: "internal server error",
				})
				c.Abort()
			}
		}()

		c.Next()
	}
}

func RequestBodyLimit(limit int64) gin.HandlerFunc {
	if limit <= 0 {
		limit = constants.MaxRequestBodyBytes
	}

	return func(c *gin.Context) {
		if c.Request != nil && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

func middlewareLogger(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}
	return logger
}
