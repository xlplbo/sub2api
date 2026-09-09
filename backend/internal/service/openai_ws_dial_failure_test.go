//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newOpenAIWSDialTransportError(cause error) *openAIWSDialError {
	return &openAIWSDialError{Err: &openAIWSHandshakeError{Err: fmt.Errorf("failed to WebSocket dial: %w", cause)}}
}

func newOpenAIWSDialFailureTestService(blockSeconds int) (*OpenAIGatewayService, *openaiTransportAccountRepoStub) {
	repo := &openaiTransportAccountRepoStub{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAITransportFailureBlockSeconds = blockSeconds
	return &OpenAIGatewayService{cfg: cfg, accountRepo: repo}, repo
}

func requireOpenAIWSDialFailover(t *testing.T, failoverErr *UpstreamFailoverError) {
	t.Helper()
	require.NotNil(t, failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.JSONEq(t, string(openAITransportFailoverBody), string(failoverErr.ResponseBody))
}

func TestOpenAITransportFailureTracker_SlidingWindow(t *testing.T) {
	var tracker openAITransportFailureTracker
	base := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)

	require.Equal(t, 1, tracker.record(152, base))
	require.Equal(t, 2, tracker.record(152, base.Add(20*time.Second)))
	require.Equal(t, 3, tracker.record(152, base.Add(40*time.Second)))
	require.Equal(t, 1, tracker.record(153, base.Add(40*time.Second)), "不同账号分别计数")
	require.Equal(t, 3, tracker.record(152, base.Add(70*time.Second)), "窗口滑动后最早一次已过期")
	require.Equal(t, 1, tracker.record(152, base.Add(200*time.Second)), "窗口内记录全部过期后重新计数")

	tracker.reset(152)
	require.Equal(t, 1, tracker.record(152, base.Add(201*time.Second)), "封禁后清零重新计数")
}

// 拨号超时与连接拒绝：每次都换号，60 秒内第 3 次才临时禁调度。
func TestHandleOpenAIWSDialTransportFailure_NetworkClassBlocksOnThird(t *testing.T) {
	cases := []struct {
		name   string
		cause  error
		reason string
	}{
		{name: "拨号超时", cause: context.DeadlineExceeded, reason: "context deadline exceeded"},
		{name: "连接拒绝", cause: errors.New("dial tcp 127.0.0.1:10809: connect: connection refused"), reason: "connection refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newOpenAIWSDialFailureTestService(120)
			account := &Account{ID: 152, Name: "proxy-down", Platform: PlatformOpenAI}
			dialErr := newOpenAIWSDialTransportError(tc.cause)

			for i := 1; i < openAITransportFailureThreshold; i++ {
				requireOpenAIWSDialFailover(t, svc.handleOpenAIWSDialTransportFailure(context.Background(), account, 1, dialErr))
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "第 %d 次失败不应封禁", i)
				require.Empty(t, repo.tempUnschedCalls)
			}

			before := time.Now()
			requireOpenAIWSDialFailover(t, svc.handleOpenAIWSDialTransportFailure(context.Background(), account, 1, dialErr))
			after := time.Now()
			require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "第 3 次失败应临时禁调度")
			require.Len(t, repo.tempUnschedCalls, 1)
			require.Equal(t, int64(152), repo.tempUnschedCalls[0].accountID)
			require.Contains(t, repo.tempUnschedCalls[0].reason, tc.reason)
			require.True(t, repo.tempUnschedCalls[0].until.After(before.Add(120*time.Second-time.Second)))
			require.True(t, repo.tempUnschedCalls[0].until.Before(after.Add(120*time.Second+time.Second)))
		})
	}
}

// 代理凭证被拒不会自愈：换号的同时一次即封 10 分钟，不走计数窗口。
func TestHandleOpenAIWSDialTransportFailure_CredentialBlocksImmediately(t *testing.T) {
	svc, repo := newOpenAIWSDialFailureTestService(120)
	account := &Account{ID: 157, Name: "proxy-expired", Platform: PlatformOpenAI}
	dialErr := newOpenAIWSDialTransportError(errors.New("socks connect tcp 1.2.3.4:1080->chatgpt.com:443: username/password authentication failed"))

	before := time.Now()
	requireOpenAIWSDialFailover(t, svc.handleOpenAIWSDialTransportFailure(context.Background(), account, 1, dialErr))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Len(t, repo.tempUnschedCalls, 1)
	require.True(t, repo.tempUnschedCalls[0].until.After(before.Add(openAITransportErrorTempUnschedDuration-time.Second)))
}

// 非持久类传输错误（如握手中途 EOF）只换号，不计入封禁阈值。
func TestHandleOpenAIWSDialTransportFailure_TransientErrorNeverBlocks(t *testing.T) {
	svc, repo := newOpenAIWSDialFailureTestService(120)
	account := &Account{ID: 153, Name: "flaky", Platform: PlatformOpenAI}
	dialErr := newOpenAIWSDialTransportError(errors.New("EOF"))

	for i := 1; i <= openAITransportFailureThreshold+1; i++ {
		requireOpenAIWSDialFailover(t, svc.handleOpenAIWSDialTransportFailure(context.Background(), account, 1, dialErr))
	}
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Empty(t, repo.tempUnschedCalls)
}

// 收到 HTTP 状态码的握手失败沿用既有分支，不在此处理。
func TestHandleOpenAIWSDialTransportFailure_StatusCodeNotHandled(t *testing.T) {
	svc, repo := newOpenAIWSDialFailureTestService(120)
	account := &Account{ID: 154, Name: "http-503", Platform: PlatformOpenAI}
	dialErr := &openAIWSDialError{StatusCode: http.StatusServiceUnavailable, Err: errors.New("service unavailable")}

	for i := 1; i <= openAITransportFailureThreshold; i++ {
		require.Nil(t, svc.handleOpenAIWSDialTransportFailure(context.Background(), account, 1, dialErr))
	}
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Empty(t, repo.tempUnschedCalls)
}

// 客户端已取消：不换号也不封禁，上游没有机会证明自己有故障。
func TestHandleOpenAIWSDialTransportFailure_ClientCanceledNotHandled(t *testing.T) {
	svc, repo := newOpenAIWSDialFailureTestService(120)
	account := &Account{ID: 155, Name: "client-gone", Platform: PlatformOpenAI}
	dialErr := newOpenAIWSDialTransportError(context.DeadlineExceeded)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := 1; i <= openAITransportFailureThreshold; i++ {
		require.Nil(t, svc.handleOpenAIWSDialTransportFailure(ctx, account, 1, dialErr))
	}
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Empty(t, repo.tempUnschedCalls)
}

// 配置为 0：网络类失败只换号，永不封禁。
func TestHandleOpenAIWSDialTransportFailure_BlockDisabled(t *testing.T) {
	svc, repo := newOpenAIWSDialFailureTestService(0)
	account := &Account{ID: 156, Name: "block-off", Platform: PlatformOpenAI}
	dialErr := newOpenAIWSDialTransportError(context.DeadlineExceeded)

	for i := 1; i <= openAITransportFailureThreshold+1; i++ {
		requireOpenAIWSDialFailover(t, svc.handleOpenAIWSDialTransportFailure(context.Background(), account, 1, dialErr))
	}
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Empty(t, repo.tempUnschedCalls)
}
