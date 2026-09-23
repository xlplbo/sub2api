package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotGroupCodexCLIOnlyRoundtrip(t *testing.T) {
	groupID := int64(51)
	apiKey := &APIKey{
		ID: 83, UserID: 41, GroupID: &groupID, Key: "sk-codex-cli-only-roundtrip", Status: StatusActive,
		User: &User{ID: 41, Status: StatusActive},
		Group: &Group{
			ID: groupID, Name: "codex-cli-only-roundtrip", Platform: PlatformOpenAI, Status: StatusActive,
			Hydrated: true, CodexCLIOnly: true, CodexCLIOnlyAllowAppServer: true,
		},
	}
	svc := &APIKeyService{}

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.CodexCLIOnly)
	require.True(t, materialized.Group.CodexCLIOnlyAllowAppServer)
	require.Equal(t, apiKeyAuthSnapshotVersion, cached.Snapshot.Version)
}

func TestAPIKeyService_RejectsV24AuthSnapshotWithoutCodexCLIOnly(t *testing.T) {
	groupID := int64(52)
	svc := &APIKeyService{}

	apiKey, used, err := svc.applyAuthCacheEntry("k-pre-codex-cli-only", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{
			Version:  24,
			APIKeyID: 1,
			UserID:   2,
			GroupID:  &groupID,
			Status:   StatusActive,
			User:     APIKeyAuthUserSnapshot{ID: 2, Status: StatusActive, Role: RoleUser},
			Group: &APIKeyAuthGroupSnapshot{
				ID: groupID, Name: "openai", Platform: PlatformOpenAI, Status: StatusActive,
				SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1,
			},
		},
	})

	require.NoError(t, err)
	require.False(t, used, "v24 snapshots predate codex_cli_only and must be rebuilt so the group gate cannot be skipped")
	require.Nil(t, apiKey)
}
