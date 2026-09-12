//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPassthroughDialTransportFailure_LegacyHTTPPolicy(t *testing.T) {
	for _, tc := range openAITransportLegacyPolicyCases() {
		t.Run(tc.name, func(t *testing.T) {
			controlCtx, cancelControl := context.WithCancel(context.Background())
			defer cancelControl()
			svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), newStagedPassthroughConn())
			repo := &openaiTransportAccountRepoStub{}
			svc.accountRepo = repo
			dialer := &openAIWSFailingDialer{err: tc.cause}
			svc.openaiWSPassthroughDialer = dialer
			account := passthroughLifecycleAccount()
			server, serverErr := startPassthroughLifecycleServer(t, controlCtx, svc, account)
			defer server.Close()

			before := time.Now()
			client := dialPassthroughLifecycleClient(t, server)
			defer func() { _ = client.CloseNow() }()
			select {
			case err := <-serverErr:
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, http.StatusBadGateway, failover.StatusCode)
				require.True(t, failover.ShouldRetryNextAccount())
				require.False(t, failover.RetryableOnSameAccount)
				require.JSONEq(t, string(openAITransportFailoverBody), string(failover.ResponseBody))
			case <-time.After(5 * time.Second):
				t.Fatal("等待 passthrough 拨号失败退出超时")
			}
			after := time.Now()
			require.Equal(t, int32(1), dialer.calls.Load())
			require.Equal(t, tc.block, svc.isOpenAIAccountRuntimeBlocked(account))
			if !tc.block {
				require.Empty(t, repo.tempUnschedCalls)
				return
			}
			require.Len(t, repo.tempUnschedCalls, 1)
			call := repo.tempUnschedCalls[0]
			require.Equal(t, account.ID, call.accountID)
			require.False(t, call.until.Before(before.Add(10*time.Minute)))
			require.False(t, call.until.After(after.Add(10*time.Minute)))
		})
	}
}
