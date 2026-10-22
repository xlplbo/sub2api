package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type compositeWSContinuationCache struct {
	mu       sync.Mutex
	bindings map[string]service.CompositeRouteBinding
}

func (c *compositeWSContinuationCache) Get(_ context.Context, key string) (*service.CompositeRouteBinding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if binding, ok := c.bindings[key]; ok {
		return &binding, nil
	}
	return nil, nil
}

func (c *compositeWSContinuationCache) Set(_ context.Context, key string, binding service.CompositeRouteBinding, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bindings[key] = binding
	return nil
}

func TestOpenAIResponsesWebSocket_CompositeContinuationReconnect(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeDedicated} {
		t.Run(mode, func(t *testing.T) {
			resolver := service.NewCompositeRouteResolver(&compositeWSRouteRepo{routes: []service.CompositeModelRoute{
				{PublicModel: "public-alias", MatchType: "exact", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5.4", Endpoint: "responses", Enabled: true},
				{PublicModel: "public-alias", MatchType: "exact", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5.5", Endpoint: "responses", RequestCondition: "has_image", Enabled: true},
			}})
			resolver.SetContinuationCache(&compositeWSContinuationCache{bindings: make(map[string]service.CompositeRouteBinding)})
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload:  `{"type":"response.create","model":"public-alias","input":[{"role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`,
				secondPayload: `{"type":"response.create","previous_response_id":"resp_usage_e2e_1","input":"continue"}`,
				group:         compositeWSGroup(), ingressMode: mode, compositeResolver: resolver,
			})
			require.Len(t, got.upstreamPayloads, 2)
			ctx := service.WithCompositeRouteUser(context.Background(), 1701)
			decision, err := resolver.ResolveRequest(ctx, compositeWSGroup().ID, "public-alias", "responses", []byte(`{"previous_response_id":"resp_usage_e2e_2","input":"continue"}`))
			require.NoError(t, err)
			require.Equal(t, "gpt-5.5", decision.UpstreamModel)
			reconnected := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload: `{"type":"response.create","model":"public-alias","previous_response_id":"resp_usage_e2e_2","input":"continue"}`,
				group:        compositeWSGroup(), ingressMode: mode, compositeResolver: resolver,
			})
			require.Equal(t, "gpt-5.5", gjson.GetBytes(reconnected.upstreamFirstPayload, "model").String())
		})
	}
}

func TestOpenAIResponsesWebSocket_CompositeImageCondition(t *testing.T) {
	for name, image := range map[string]string{
		"message":     `[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,abc"}]}]`,
		"custom-tool": `[{"type":"custom_tool_call_output","call_id":"call_1","output":{"content":[{"type":"input_image","image_url":"data:image/png;base64,abc"}]}}]`,
	} {
		for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeDedicated} {
			for _, imageFirst := range []bool{true, false} {
				t.Run(name+"/"+mode+map[bool]string{true: "/image-first", false: "/image-later"}[imageFirst], func(t *testing.T) {
					first, second := `"hello"`, image
					if imageFirst {
						first, second = image, `"again"`
					}
					got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
						firstPayload:  `{"type":"response.create","model":"public-alias","input":` + first + `}`,
						secondPayload: `{"type":"response.create","input":` + second + `}`,
						group:         compositeWSGroup(), ingressMode: mode,
						compositeResolver: service.NewCompositeRouteResolver(&compositeWSRouteRepo{routes: []service.CompositeModelRoute{
							{PublicModel: "public-alias", MatchType: "exact", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5.4", Endpoint: "responses", Enabled: true},
							{PublicModel: "public-alias", MatchType: "exact", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5.5", Endpoint: "responses", RequestCondition: "has_image", Enabled: true},
						}}),
						secondTurnCloseExpected: !imageFirst, closeReason: "image route requires reconnect",
					})
					if imageFirst {
						require.Len(t, got.upstreamPayloads, 2)
						for _, payload := range got.upstreamPayloads {
							require.Equal(t, "gpt-5.5", gjson.GetBytes(payload, "model").String())
						}
					}
				})
			}
		}
	}
}
