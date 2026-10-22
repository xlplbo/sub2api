package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type compositeCountingRouteRepo struct {
	CompositeModelRouteRepository
	routes []CompositeModelRoute
	calls  int
}

type compositeContinuationCacheStub struct {
	bindings map[string]CompositeRouteBinding
	err      error
}

func (c *compositeContinuationCacheStub) Get(_ context.Context, key string) (*CompositeRouteBinding, error) {
	if c.err != nil {
		return nil, c.err
	}
	if binding, ok := c.bindings[key]; ok {
		return &binding, nil
	}
	return nil, nil
}

func (c *compositeContinuationCacheStub) Set(_ context.Context, key string, binding CompositeRouteBinding, _ time.Duration) error {
	if c.err != nil {
		return c.err
	}
	c.bindings[key] = binding
	return nil
}

func TestCompositeContinuationPreservesDestinationAndIsolation(t *testing.T) {
	for _, imageFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "image"}[imageFirst], func(t *testing.T) {
			repo := &compositeCountingRouteRepo{routes: []CompositeModelRoute{
				{ID: 1, PublicModel: "glm-", MatchType: "prefix", TargetPlatform: PlatformOpenAI, UpstreamModel: "text-model", Endpoint: "responses", Enabled: true},
				{ID: 2, PublicModel: "glm-", MatchType: "prefix", TargetPlatform: PlatformOpenAI, UpstreamModel: "vision-model", Endpoint: "responses", RequestCondition: "has_image", Enabled: true},
			}}
			resolver := NewCompositeRouteResolver(repo)
			cache := &compositeContinuationCacheStub{bindings: make(map[string]CompositeRouteBinding)}
			resolver.SetContinuationCache(cache)
			ctx := WithCompositeRouteUser(context.Background(), 42)
			first := []byte(`{"input":"hello"}`)
			want := "text-model"
			if imageFirst {
				first = []byte(`{"input":[{"content":[{"type":"input_image","file_id":"file_1"}]}]}`)
				want = "vision-model"
			}
			decision, err := resolver.ResolveRequest(ctx, 7, "glm-5.3", "responses", first)
			require.NoError(t, err)
			require.Equal(t, want, decision.UpstreamModel)
			require.NoError(t, resolver.RememberResponse(ctx, decision, "resp_1", "conv_1"))
			for _, reference := range []string{`"previous_response_id":"resp_1"`, `"conversation":"conv_1"`, `"conversation":{"id":"conv_1"}`} {
				body := []byte(`{` + reference + `,"input":"continue"}`)
				repo.calls = 0
				got, err := resolver.ResolveRequest(ctx, 7, "glm-5.3", "responses", body)
				require.NoError(t, err)
				require.Equal(t, want, got.UpstreamModel)
				require.Equal(t, 1, repo.calls)
				for _, other := range []struct {
					user, group int64
					model       string
				}{
					{43, 7, "glm-5.3"}, {42, 8, "glm-5.3"}, {42, 7, "glm-5.2"},
				} {
					_, err = resolver.ResolveRequest(WithCompositeRouteUser(context.Background(), other.user), other.group, other.model, "responses", body)
					require.ErrorIs(t, err, ErrCompositeRouteHistoryRequired)
				}
			}
			body := []byte(`{"previous_response_id":"resp_1","input":[{"content":[{"type":"input_image","file_id":"file_2"}]}]}`)
			got, err := resolver.ResolveRequest(ctx, 7, "glm-5.3", "responses", body)
			if imageFirst {
				require.NoError(t, err)
				require.Equal(t, "vision-model", got.UpstreamModel)
			} else {
				require.ErrorIs(t, err, ErrCompositeRouteHistoryRequired)
			}
			continuation := []byte(`{"previous_response_id":"resp_1","input":"continue"}`)
			cache.err = errors.New("cache unavailable")
			_, err = resolver.ResolveRequest(ctx, 7, "glm-5.3", "responses", continuation)
			require.ErrorContains(t, err, "cache unavailable")
			cache.err = nil
			repo.routes[0].UpstreamModel, repo.routes[1].UpstreamModel = "new-text", "new-vision"
			_, err = resolver.ResolveRequest(ctx, 7, "glm-5.3", "responses", continuation)
			require.ErrorIs(t, err, ErrCompositeRouteHistoryRequired)
			repo.routes = repo.routes[:1]
			_, err = resolver.ResolveRequest(ctx, 7, "glm-5.3", "responses", continuation)
			require.ErrorIs(t, err, ErrCompositeRouteHistoryRequired)
		})
	}
}

func (r *compositeCountingRouteRepo) ListByGroup(context.Context, int64, bool) ([]CompositeModelRoute, error) {
	r.calls++
	return r.routes, nil
}

func TestCompositeRequestLoadsRoutesOnce(t *testing.T) {
	for _, reference := range []string{`"previous_response_id":"resp_1"`, `"conversation":{"id":"conv_1"}`} {
		t.Run(reference, func(t *testing.T) {
			repo := &compositeCountingRouteRepo{routes: []CompositeModelRoute{
				{ID: 1, PublicModel: "glm-5.3", MatchType: "exact", TargetPlatform: PlatformOpenAI, UpstreamModel: "glm-5.3", Endpoint: "responses", Enabled: true},
			}}
			resolver := NewCompositeRouteResolver(repo)
			decision, err := resolver.ResolveRequest(context.Background(), 7, "glm-5.3", "responses", []byte(`{`+reference+`,"input":"continue"}`))
			require.NoError(t, err)
			require.Equal(t, "glm-5.3", decision.UpstreamModel)
			require.Equal(t, 1, repo.calls)
		})
	}
}
