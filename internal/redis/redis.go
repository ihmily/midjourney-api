package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/trae/midjourney-api/internal/config"
)

func Init(cfg *config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	})

	ctx := context.Background()
	_, err := client.Ping(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to connect redis: %w", err)
	}

	return client, nil
}
