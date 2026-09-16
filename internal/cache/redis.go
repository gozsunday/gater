package cache

import (
	"context"
	"fmt"

	"github.com/gozsunday/gater/internal/config"
	"github.com/redis/go-redis/v9"
)

func NewRedisClient(ctx context.Context, cfg *config.Config) (*redis.Client, error) {
	// get redis options from the redis URL
	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("cache: parse redis url: %w", err)
	}

	// get & ping redis client
	client := redis.NewClient(opt)
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("cache: ping redis: %w", err)
	}

	return client, nil
}
