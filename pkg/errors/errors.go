package errors

import (
	"fmt"
)

type ErrorCode string

const (
	// Universal error codes
	ErrCodeSuccess      ErrorCode = "SUCCESS"
	ErrCodeInvalidInput ErrorCode = "INVALID_INPUT"
	ErrCodeNotFound     ErrorCode = "NOT_FOUND"
	ErrCodeInternal     ErrorCode = "INTERNAL_ERROR"
	ErrCodeUnauthorized ErrorCode = "UNAUTHORIZED"
	ErrCodeForbidden    ErrorCode = "FORBIDDEN"

	// Account related error codes
	ErrCodeAccountNotFound      ErrorCode = "ACCOUNT_NOT_FOUND"
	ErrCodeAccountAlreadyExists ErrorCode = "ACCOUNT_ALREADY_EXISTS"
	ErrCodeAccountUnavailable   ErrorCode = "ACCOUNT_UNAVAILABLE"
	ErrCodeAccountUnhealthy     ErrorCode = "ACCOUNT_UNHEALTHY"
	ErrCodeAccountLimitReached  ErrorCode = "ACCOUNT_LIMIT_REACHED"

	// Task related error codes
	ErrCodeTaskNotFound        ErrorCode = "TASK_NOT_FOUND"
	ErrCodeTaskAlreadyTerminal ErrorCode = "TASK_ALREADY_TERMINAL"
	ErrCodeTaskNotCompleted    ErrorCode = "TASK_NOT_COMPLETED"
	ErrCodeTaskCreateFailed    ErrorCode = "TASK_CREATE_FAILED"
	ErrCodeTaskProcessFailed   ErrorCode = "TASK_PROCESS_FAILED"

	// Discord related error codes
	ErrCodeDiscordAPIError ErrorCode = "DISCORD_API_ERROR"
	ErrCodeDiscordTimeout  ErrorCode = "DISCORD_TIMEOUT"

	// Database related error codes
	ErrCodeDatabaseError ErrorCode = "DATABASE_ERROR"
	ErrCodeRedisError    ErrorCode = "REDIS_ERROR"
)

type AppError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Detail  string    `json:"detail,omitempty"`
	Err     error     `json:"-"`
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error {
	return e.Err
}

func New(code ErrorCode, message string) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
	}
}

func Wrap(code ErrorCode, message string, err error) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

func (e *AppError) WithDetail(detail string) *AppError {
	e.Detail = detail
	return e
}

func NewInvalidInput(message string) *AppError {
	return New(ErrCodeInvalidInput, message)
}

func NewNotFound(message string) *AppError {
	return New(ErrCodeNotFound, message)
}

func NewInternal(message string, err error) *AppError {
	return Wrap(ErrCodeInternal, message, err)
}

func NewAccountNotFound(accountID uint) *AppError {
	return New(ErrCodeAccountNotFound, fmt.Sprintf("Account not found: %d", accountID))
}

func NewAccountAlreadyExists(guildID, channelID string) *AppError {
	return New(ErrCodeAccountAlreadyExists,
		fmt.Sprintf("Account already exists for guild %s channel %s", guildID, channelID))
}

func NewAccountUnavailable(reason string) *AppError {
	return New(ErrCodeAccountUnavailable, reason)
}

func NewTaskNotFound(taskID string) *AppError {
	return New(ErrCodeTaskNotFound, fmt.Sprintf("Task not found: %s", taskID))
}

func NewTaskAlreadyTerminal(taskID string, status string) *AppError {
	return New(ErrCodeTaskAlreadyTerminal, fmt.Sprintf("Task already terminal: %s status=%s", taskID, status))
}

func NewDatabaseError(err error) *AppError {
	return Wrap(ErrCodeDatabaseError, "Database error", err)
}
