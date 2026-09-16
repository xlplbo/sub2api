package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newLegacySchedulerDecisionTestAccounts(groupID int64, secondWSEnabled bool) []Account {
	return []Account{
		{
			ID: 38101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
			GroupIDs: []int64{groupID},
			Extra:    map[string]any{"openai_apikey_responses_websockets_v2_enabled": true},
		},
		{
			ID: 38102, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
			GroupIDs: []int64{groupID},
			Extra:    map[string]any{"openai_apikey_responses_websockets_v2_enabled": secondWSEnabled},
		},
	}
}

func newLegacySchedulerDecisionTestService(accounts []Account, loadBatch bool, concurrency schedulerTestConcurrencyCache) *OpenAIGatewayService {
	cfg := newSchedulerTestOpenAIWSV2Config()
	cfg.Gateway.Scheduling.LoadBatchEnabled = loadBatch
	return &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrency),
	}
}

func releaseLegacySchedulerDecisionSelection(selection *AccountSelectionResult) {
	if selection != nil && selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestLegacySchedulerDecision_StickySessionLayer(t *testing.T) {
	ctx := context.Background()
	groupID := int64(38100)
	for _, loadBatch := range []bool{true, false} {
		name := "load_batch"
		if !loadBatch {
			name = "priority_lru"
		}
		t.Run(name, func(t *testing.T) {
			svc := newLegacySchedulerDecisionTestService(newLegacySchedulerDecisionTestAccounts(groupID, true), loadBatch, schedulerTestConcurrencyCache{})
			sessionHash := "legacy-sticky-session"
			require.NoError(t, svc.setStickySessionAccountID(ctx, &groupID, sessionHash, 38102, time.Hour))

			selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
				ctx, &groupID, "", sessionHash, "gpt-5.1", nil,
				OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true,
			)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.NotNil(t, selection.Account)
			releaseLegacySchedulerDecisionSelection(selection)
			require.Equal(t, int64(38102), selection.Account.ID, "sticky binding must win over priority order")
			require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
			require.True(t, decision.StickySessionHit)
			require.False(t, decision.StickyPreviousHit)
			require.Equal(t, int64(38102), decision.SelectedAccountID)
			require.Equal(t, AccountTypeAPIKey, decision.SelectedAccountType)

			selection, decision, err = svc.SelectAccountWithSchedulerForCapability(
				ctx, &groupID, "", "", "gpt-5.1", nil,
				OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true,
			)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.NotNil(t, selection.Account)
			releaseLegacySchedulerDecisionSelection(selection)
			require.Equal(t, int64(38101), selection.Account.ID)
			require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
			require.False(t, decision.StickySessionHit)
			require.Equal(t, int64(38101), decision.SelectedAccountID)
		})
	}
}

func TestLegacySchedulerDecision_PreviousResponseRouting(t *testing.T) {
	ctx := context.Background()
	groupID := int64(38110)
	responseID := "resp_legacy_route"

	t.Run("routes to the account owning the response", func(t *testing.T) {
		svc := newLegacySchedulerDecisionTestService(newLegacySchedulerDecisionTestAccounts(groupID, true), true, schedulerTestConcurrencyCache{})
		require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, responseID, 38102, time.Hour))

		selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
			ctx, &groupID, responseID, "legacy-prev-session", "gpt-5.1", nil,
			OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityChatCompletions, false, true, true,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		releaseLegacySchedulerDecisionSelection(selection)
		require.Equal(t, int64(38102), selection.Account.ID, "previous_response_id must route to the owning account")
		require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
		require.True(t, decision.StickyPreviousHit)
		require.False(t, decision.StickySessionHit)
		require.Equal(t, int64(38102), decision.SelectedAccountID)
	})

	t.Run("excluded owner falls back to load balance", func(t *testing.T) {
		svc := newLegacySchedulerDecisionTestService(newLegacySchedulerDecisionTestAccounts(groupID, true), true, schedulerTestConcurrencyCache{})
		require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, responseID, 38102, time.Hour))

		selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
			ctx, &groupID, responseID, "", "gpt-5.1", map[int64]struct{}{38102: {}},
			OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, true, true,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		releaseLegacySchedulerDecisionSelection(selection)
		require.Equal(t, int64(38101), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
		require.False(t, decision.StickyPreviousHit)
	})

	t.Run("transport-incompatible owner releases its slot and falls back", func(t *testing.T) {
		released := make([]int64, 0)
		svc := newLegacySchedulerDecisionTestService(newLegacySchedulerDecisionTestAccounts(groupID, false), true, schedulerTestConcurrencyCache{releasedIDs: &released})
		require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, responseID, 38102, time.Hour))

		selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
			ctx, &groupID, responseID, "", "gpt-5.1", nil,
			OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityChatCompletions, false, true, true,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		releaseLegacySchedulerDecisionSelection(selection)
		require.Equal(t, int64(38101), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
		require.False(t, decision.StickyPreviousHit)
		require.Contains(t, released, int64(38102), "the owning account's slot must be released after the transport check fails")
	})
}
