package response

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/redact"
)

type Response struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
	Detail  string      `json:"detail,omitempty"`
}

func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code:    string(apperrors.ErrCodeSuccess),
		Message: "success",
		Data:    data,
	})
}

func Created(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, Response{
		Code:    string(apperrors.ErrCodeSuccess),
		Message: "created successfully",
		Data:    data,
	})
}

func Error(c *gin.Context, err error) {
	var appErr *apperrors.AppError
	if errors.As(err, &appErr) {
		statusCode := getHTTPStatusCode(appErr.Code)
		c.JSON(statusCode, Response{
			Code:    string(appErr.Code),
			Message: publicErrorMessage(appErr.Message),
			Detail:  publicErrorDetail(appErr.Detail, statusCode),
		})
		return
	}

	if isClientInputError(err) {
		c.JSON(http.StatusBadRequest, Response{
			Code:    string(apperrors.ErrCodeInvalidInput),
			Message: "invalid input",
			Detail:  publicErrorDetail(err.Error(), http.StatusBadRequest),
		})
		return
	}

	c.JSON(http.StatusInternalServerError, Response{
		Code:    string(apperrors.ErrCodeInternal),
		Message: "internal server error",
	})
}

func publicErrorMessage(message string) string {
	redacted := redact.Text(message)
	if redacted == "" {
		return message
	}
	return redacted
}

func publicErrorDetail(detail string, statusCode int) string {
	if statusCode >= http.StatusInternalServerError {
		return ""
	}
	return redact.Text(detail)
}

func getHTTPStatusCode(code apperrors.ErrorCode) int {
	switch code {
	case apperrors.ErrCodeSuccess:
		return http.StatusOK
	case apperrors.ErrCodeInvalidInput:
		return http.StatusBadRequest
	case apperrors.ErrCodeNotFound,
		apperrors.ErrCodeAccountNotFound,
		apperrors.ErrCodeTaskNotFound:
		return http.StatusNotFound
	case apperrors.ErrCodeUnauthorized:
		return http.StatusUnauthorized
	case apperrors.ErrCodeForbidden:
		return http.StatusForbidden
	case apperrors.ErrCodeAccountUnavailable,
		apperrors.ErrCodeAccountAlreadyExists,
		apperrors.ErrCodeAccountUnhealthy,
		apperrors.ErrCodeAccountLimitReached,
		apperrors.ErrCodeTaskAlreadyTerminal,
		apperrors.ErrCodeTaskNotCompleted:
		return http.StatusConflict
	case apperrors.ErrCodeDatabaseError,
		apperrors.ErrCodeRedisError,
		apperrors.ErrCodeDiscordAPIError,
		apperrors.ErrCodeDiscordTimeout,
		apperrors.ErrCodeTaskCreateFailed,
		apperrors.ErrCodeTaskProcessFailed,
		apperrors.ErrCodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func isClientInputError(err error) bool {
	var validationErrs validator.ValidationErrors
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError
	var numErr *strconv.NumError

	return errors.As(err, &validationErrs) ||
		errors.As(err, &syntaxErr) ||
		errors.As(err, &typeErr) ||
		errors.As(err, &maxBytesErr) ||
		errors.As(err, &numErr) ||
		errors.Is(err, io.EOF) ||
		isJSONUnknownFieldError(err)
}

func isJSONUnknownFieldError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "json: unknown field ")
}
