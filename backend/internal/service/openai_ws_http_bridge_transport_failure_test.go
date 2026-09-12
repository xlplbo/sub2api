//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSHTTPBridgeTransportFailure_LegacyPolicyAllTurns(t *testing.T) {
	for _, tc := range openAITransportLegacyPolicyCases() {
		for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
			for _, turn := range []int{1, 2} {
				t.Run(fmt.Sprintf("%s/%s/turn=%d", tc.name, accountType, turn), func(t *testing.T) {
					repo := &openaiTransportAccountRepoStub{}
					upstream := &httpUpstreamRecorder{err: tc.cause}
					svc := &OpenAIGatewayService{
						cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo,
					}
					account := &Account{ID: 720, Platform: PlatformOpenAI, Type: accountType, Concurrency: 1}
					c, rec := newOpenAITransportErrTestContext()
					payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
					writes := 0
					before := time.Now()
					_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
						context.Background(), c, account, "test-token", payload, len(payload),
						"gpt-5", "", "", "", "", turn,
						func([]byte) error { writes++; return nil },
					)
					after := time.Now()
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.Equal(t, http.StatusBadGateway, failover.StatusCode)
					require.True(t, failover.ShouldRetryNextAccount())
					require.False(t, failover.RetryableOnSameAccount)
					require.Zero(t, writes)
					require.Zero(t, rec.Body.Len())
					require.Len(t, upstream.requests, 1)
					require.Equal(t, tc.block, svc.isOpenAIAccountRuntimeBlocked(account))
					if !tc.block {
						require.Empty(t, repo.tempUnschedCalls)
						return
					}
					require.Len(t, repo.tempUnschedCalls, 1)
					until := repo.tempUnschedCalls[0].until
					require.False(t, until.Before(before.Add(10*time.Minute)))
					require.False(t, until.After(after.Add(10*time.Minute)))
				})
			}
		}
	}
}
