//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCompositeModelRouteRepositoryImageCondition(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	client := tx.Client()
	group := mustCreateGroup(t, client, &service.Group{Name: "image-routes", Platform: service.PlatformComposite})
	repo := NewCompositeModelRouteRepository(client)
	base := service.CompositeModelRoute{
		GroupID: group.ID, PublicModel: "glm-5.3", MatchType: "exact", Endpoint: "responses",
		TargetPlatform: service.PlatformOpenAI, UpstreamModel: "glm-5.3", RequestCondition: "any", Enabled: true,
	}
	require.NoError(t, repo.Create(ctx, &base))
	image := base
	image.ID = 0
	image.RequestCondition = "has_image"
	image.UpstreamModel = "vision-model"
	require.NoError(t, repo.Create(ctx, &image))
	rows, err := repo.ListByGroup(ctx, group.ID, false)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "has_image", rows[1].RequestCondition)
	image.UpstreamModel = "other-vision-model"
	require.NoError(t, repo.Update(ctx, &image))
	require.Equal(t, "has_image", image.RequestCondition)
	require.NoError(t, repo.Delete(ctx, image.ID))
	image.ID = 0
	require.NoError(t, repo.Create(ctx, &image), "a soft-deleted image rule must not block its replacement")
}
