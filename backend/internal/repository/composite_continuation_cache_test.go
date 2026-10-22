package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCompositeContinuationCacheSurvivesResolverReplacementAndExpires(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewCompositeContinuationCache(client)
	ctx := context.Background()
	binding := service.CompositeRouteBinding{TargetPlatform: "openai", UpstreamModel: "vision-model"}
	require.NoError(t, cache.Set(ctx, "composite:test", binding, time.Hour))
	other := NewCompositeContinuationCache(client)
	got, err := other.Get(ctx, "composite:test")
	require.NoError(t, err)
	require.Equal(t, &binding, got)
	server.FastForward(time.Hour + time.Second)
	got, err = other.Get(ctx, "composite:test")
	require.NoError(t, err)
	require.Nil(t, got)
}
