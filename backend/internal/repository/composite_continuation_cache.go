package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

type compositeContinuationCache struct {
	client *redis.Client
}

func NewCompositeContinuationCache(client *redis.Client) service.CompositeContinuationCache {
	return &compositeContinuationCache{client: client}
}

func (c *compositeContinuationCache) Get(ctx context.Context, key string) (*service.CompositeRouteBinding, error) {
	payload, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var binding service.CompositeRouteBinding
	if err := json.Unmarshal(payload, &binding); err != nil {
		return nil, err
	}
	return &binding, nil
}

func (c *compositeContinuationCache) Set(ctx context.Context, key string, binding service.CompositeRouteBinding, ttl time.Duration) error {
	payload, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key, payload, ttl).Err()
}
