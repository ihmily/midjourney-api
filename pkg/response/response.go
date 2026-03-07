package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
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
	if appErr, ok := err.(*apperrors.AppError); ok {
		statusCode := getHTTPStatusCode(appErr.Code)
		c.JSON(statusCode, Response{
			Code:    string(appErr.Code),
			Message: appErr.Message,
			Detail:  appErr.Detail,
		})
		return
	}

	c.JSON(http.StatusInternalServerError, Response{
		Code:    string(apperrors.ErrCodeInternal),
		Message: "internal server error",
		Detail:  err.Error(),
	})
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
		apperrors.ErrCodeAccountUnhealthy,
		apperrors.ErrCodeAccountLimitReached,
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
