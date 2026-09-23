package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// newGroupCodexCLIOnlyWSTestClient 起一个只含 ResponsesWebSocket 的网关，分组无可用账号：
// 分组门放行的连接随后在选号阶段以「no available account」关闭，据此与分组门拒绝区分。
func newGroupCodexCLIOnlyWSTestClient(t *testing.T, group *service.Group, header http.Header) *coderws.Conn {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gateway := service.NewOpenAIGatewayService(&openAIWSTurnBudgetAccountRepo{}, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billing, openAIWSTurnBudgetHTTPClient{},
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	apiKey := &service.APIKey{
		ID: 1803, GroupID: &group.ID, User: &service.User{ID: 1703, Status: service.StatusActive}, Group: group,
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	finished := make(chan struct{})
	router.GET("/v1/responses", func(c *gin.Context) {
		h.ResponsesWebSocket(c)
		close(finished)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: header})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.CloseNow()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("websocket handler did not exit")
		}
	})
	return conn
}

func TestOpenAIWSGroupCodexCLIOnly(t *testing.T) {
	codexGroup := func(enabled bool) *service.Group {
		return &service.Group{ID: 4203, Platform: service.PlatformOpenAI, Status: service.StatusActive, CodexCLIOnly: enabled}
	}
	scriptHeader := http.Header{"User-Agent": []string{"ctrl-probe/1"}}
	officialHeader := http.Header{
		"User-Agent":        []string{"codex_cli_rs/0.153.4 (Windows 11; x86_64) WindowsTerminal"},
		"Originator":        []string{"codex_cli_rs"},
		"X-Codex-Window-Id": []string{"window-1"},
	}
	firstFrame := []byte(`{"type":"response.create","model":"gpt-5.1","input":"hello"}`)

	t.Run("非官方客户端：先发 forbidden_error 帧，再以 1008 关闭", func(t *testing.T) {
		conn := newGroupCodexCLIOnlyWSTestClient(t, codexGroup(true), scriptHeader)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, conn.Write(ctx, coderws.MessageText, firstFrame))

		_, payload, err := conn.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, "error", gjson.GetBytes(payload, "type").String())
		require.Equal(t, "forbidden_error", gjson.GetBytes(payload, "error.type").String())
		require.Equal(t, service.CodexGroupOfficialClientsOnlyMessage, gjson.GetBytes(payload, "error.message").String())

		_, _, err = conn.Read(ctx)
		var closeErr coderws.CloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
		require.Equal(t, service.CodexGroupOfficialClientsOnlyMessage, closeErr.Reason)
	})

	for name, tc := range map[string]struct {
		group  *service.Group
		header http.Header
	}{
		"官方客户端：通过分组门":     {group: codexGroup(true), header: officialHeader},
		"分组未开：非官方客户端不受影响": {group: codexGroup(false), header: scriptHeader},
	} {
		t.Run(name, func(t *testing.T) {
			conn := newGroupCodexCLIOnlyWSTestClient(t, tc.group, tc.header)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, firstFrame))

			_, _, err := conn.Read(ctx)
			var closeErr coderws.CloseError
			require.ErrorAs(t, err, &closeErr)
			require.NotEqual(t, service.CodexGroupOfficialClientsOnlyMessage, closeErr.Reason,
				"通过分组门后应进入选号阶段（本测试无账号）")
		})
	}
}
