package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompositeImageRouteSelection(t *testing.T) {
	routes := []CompositeModelRoute{
		{ID: 1, GroupID: 7, PublicModel: "glm-5.3", MatchType: "exact", TargetPlatform: PlatformOpenAI, UpstreamModel: "glm-5.3", Endpoint: "responses", Priority: 1, Enabled: true},
		{ID: 2, GroupID: 7, PublicModel: "glm-5.3", MatchType: "exact", TargetPlatform: PlatformOpenAI, UpstreamModel: "vision-model", Endpoint: "responses", RequestCondition: "has_image", Priority: 100, Enabled: true},
	}
	resolver := NewCompositeRouteResolver(compositeRouteRepoStub{routes: routes})
	for _, hasImage := range []bool{false, true} {
		got, err := resolver.ResolveWithImage(context.Background(), 7, "glm-5.3", "responses", hasImage)
		require.NoError(t, err)
		require.Equal(t, "glm-5.3", got.PublicModel)
		want := "glm-5.3"
		if hasImage {
			want = "vision-model"
		}
		require.Equal(t, want, got.UpstreamModel)
	}
	models, err := resolver.ListExactPublicModels(context.Background(), 7, "responses", true)
	require.NoError(t, err)
	require.Equal(t, []string{"glm-5.3"}, models)

	routes[1].Endpoint = "any"
	got, err := resolver.ResolveWithImage(context.Background(), 7, "glm-5.3", "responses", true)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.Route.ID, "endpoint specificity must still win")
	routes[1].Endpoint = "responses"
	routes[1].Enabled = false
	got, err = resolver.ResolveWithImage(context.Background(), 7, "glm-5.3", "responses", true)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.Route.ID)
}

func TestCompositeImageRouteValidation(t *testing.T) {
	for _, condition := range []string{"", "any", "has_image", "typo"} {
		route, err := compositeRouteFromInput(7, CompositeRouteInput{
			PublicModel: "glm-5.3", TargetPlatform: PlatformOpenAI, RequestCondition: condition,
		})
		if condition == "typo" {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		if condition == "" {
			condition = "any"
		}
		require.Equal(t, condition, route.RequestCondition)
	}
}
