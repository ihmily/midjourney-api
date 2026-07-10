package redis

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/pkg/redact"
)

func Init(cfg *config.RedisConfig) (*redis.Client, error) {
	if err := validateRedisConfig(cfg); err != nil {
		return nil, err
	}

	host, err := cfg.NormalizedHost()
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(&redis.Options{
		Addr:     redisAddress(host, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.Ping(ctx).Result()
	if err != nil {
		_ = client.Close()
		return nil, redisConnectError(err)
	}

	return client, nil
}

func validateRedisConfig(cfg *config.RedisConfig) error {
	if cfg == nil {
		return fmt.Errorf("redis config is required")
	}
	return cfg.Validate()
}

func redisAddress(host string, port int) string {
	normalized, err := config.NormalizeRedisHost(host)
	if err == nil {
		host = normalized
	} else {
		host = strings.TrimSpace(host)
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func redisConnectError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("failed to connect redis: %w", redact.Error(err))
}
