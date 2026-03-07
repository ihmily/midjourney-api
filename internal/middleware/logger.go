package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func RequestLogger(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := uuid.New().String()
		c.Set("request_id", requestID)

		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

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
			zap.String("user_agent", c.Request.UserAgent()),
		}

		if len(c.Errors) > 0 {
			fields = append(fields, zap.String("errors", c.Errors.String()))
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

func Recovery(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				requestID, _ := c.Get("request_id")
				logger.Error("Panic recovered",
					zap.Any("error", err),
					zap.String("request_id", requestID.(string)),
					zap.String("path", c.Request.URL.Path),
					zap.Stack("stack"),
				)

				c.JSON(500, gin.H{
					"code":    "INTERNAL_ERROR",
					"message": "Internal server error",
				})
				c.Abort()
			}
		}()

		c.Next()
	}
}
