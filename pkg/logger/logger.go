package logger

import (
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func Init(mode string) (*zap.Logger, error) {
	var config zap.Config

	if mode == "release" {
		config = zap.NewProductionConfig()
		config.Encoding = "json"
		config.EncoderConfig.TimeKey = "timestamp"
		config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		config.EncoderConfig.EncodeDuration = zapcore.SecondsDurationEncoder
		config.EncoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
	} else {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		config.EncoderConfig.EncodeTime = func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(t.Format("2006-01-02 15:04:05.000"))
		}
	}

	config.EncoderConfig.CallerKey = "caller"
	config.EncoderConfig.MessageKey = "message"
	config.EncoderConfig.LevelKey = "level"
	config.EncoderConfig.StacktraceKey = "stacktrace"

	logger, err := config.Build(
		zap.AddCallerSkip(0),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	if err != nil {
		return nil, err
	}

	return logger, nil
}
