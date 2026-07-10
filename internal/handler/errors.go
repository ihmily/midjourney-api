package handler

import (
	"fmt"

	apperrors "github.com/trae/midjourney-api/pkg/errors"
)

func handlerInternalError(format string, args ...interface{}) error {
	return apperrors.NewInternal("internal server error", fmt.Errorf(format, args...))
}
