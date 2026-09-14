//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAuthCacheInvalidationTrigger_CodexWSOnly(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	group := mustCreateGroup(t, integrationEntClient, &service.Group{
		Name: fmt.Sprintf("codex-ws-trigger-%d", suffix), Platform: service.PlatformOpenAI, RateMultiplier: 1,
	})
	user := mustCreateUser(t, integrationEntClient, &service.User{
		Email: fmt.Sprintf("codex-ws-trigger-%d@example.com", suffix), Concurrency: 5,
	})
	groupID := group.ID
	keyValue := fmt.Sprintf("sk-codex-ws-trigger-%d", suffix)
	key := &service.APIKey{UserID: user.ID, GroupID: &groupID, Key: keyValue, Name: "codex-ws-trigger", Status: service.StatusActive}
	require.NoError(t, NewAPIKeyRepository(integrationEntClient, integrationDB).Create(ctx, key))

	sum := sha256.Sum256([]byte(keyValue))
	cacheKey := hex.EncodeToString(sum[:])
	clear := func() {
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM auth_cache_invalidation_outbox WHERE cache_key = $1", cacheKey)
		require.NoError(t, err)
	}
	t.Cleanup(clear)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM api_keys WHERE id = $1", key.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", user.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", group.ID)
		require.NoError(t, err)
	})

	for _, tc := range []struct {
		name      string
		enabled   bool
		wantCount int
	}{
		{name: "enable", enabled: true, wantCount: 1},
		{name: "unchanged_enabled", enabled: true, wantCount: 0},
		{name: "disable", enabled: false, wantCount: 1},
		{name: "unchanged_disabled", enabled: false, wantCount: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clear()
			_, err := integrationDB.ExecContext(ctx, "UPDATE groups SET codex_ws_only = $1 WHERE id = $2", tc.enabled, group.ID)
			require.NoError(t, err)
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key = $1", cacheKey).Scan(&count))
			require.Equal(t, tc.wantCount, count)
		})
	}
}
