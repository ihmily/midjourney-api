package handler

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
)

func init() {
	binding.EnableDecoderDisallowUnknownFields = true
}

func bindStrictJSON(c *gin.Context, dst interface{}) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return apperrors.NewInvalidInput("request body is required")
	}

	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return apperrors.NewInvalidInput("request body is required")
		}
		return err
	}

	if err := ensureSingleJSONValue(decoder); err != nil {
		return err
	}

	return binding.Validator.ValidateStruct(dst)
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var extra interface{}
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return apperrors.NewInvalidInput("request body must contain a single JSON value")
}
