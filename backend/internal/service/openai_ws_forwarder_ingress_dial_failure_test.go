package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIWSFailingDialer struct {
	err   error
	calls atomic.Int32
}

func (d *openAIWSFailingDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	d.calls.Add(1)
	return nil, 0, nil, d.err
}

// 场景：账号所绑代理断开，WS 拨号在传输层失败（无 HTTP 状态码）。
// 每次请求都应返回可换号的错误交给 handler 切账号，60 秒内第 3 次失败后账号被临时禁调度。
func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_DialTransportFailureFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAITransportFailureBlockSeconds = 120

	dialer := &openAIWSFailingDialer{
		err: &openAIWSHandshakeError{Err: errors.New("failed to WebSocket dial: dial tcp 127.0.0.1:10809: connect: connection refused")},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	defer pool.Close()

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          452,
		Name:        "openai-ingress-dial-refused",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req
		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	runFailedSession := func() *UpstreamFailoverError {
		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
		cancelDial()
		require.NoError(t, err)
		defer func() { _ = clientConn.CloseNow() }()

		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
		cancelWrite()
		require.NoError(t, err)

		select {
		case serverErr := <-serverErrCh:
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, serverErr, &failoverErr, "拨号传输失败应返回可换号的错误")
			return failoverErr
		case <-time.After(5 * time.Second):
			t.Fatal("等待 ingress websocket 结束超时")
			return nil
		}
	}

	for i := 1; i <= openAITransportFailureThreshold-1; i++ {
		failoverErr := runFailedSession()
		require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
		require.True(t, failoverErr.ShouldRetryNextAccount())
		require.JSONEq(t, string(openAITransportFailoverBody), string(failoverErr.ResponseBody))
		require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "第 %d 次失败不应封禁", i)
	}
	failoverErr := runFailedSession()
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "60 秒内第 3 次失败应临时禁调度")
	require.Equal(t, int32(openAITransportFailureThreshold), dialer.calls.Load())
}
