package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func codexCLIOnlyGroup(enabled bool) *service.Group {
	return &service.Group{ID: 9, Platform: service.PlatformOpenAI, CodexCLIOnly: enabled}
}

// TestGatewayRoutesGroupCodexCLIOnlyMountedOnEveryGatewayChain 断言每条挂了分组模型白名单的
// 网关链都在其前面挂了分组 Codex 官方客户端准入（源码级断言，约定同 allowlist 覆盖测试）。
func TestGatewayRoutesGroupCodexCLIOnlyMountedOnEveryGatewayChain(t *testing.T) {
	routeSource, err := os.ReadFile("gateway.go")
	require.NoError(t, err)
	source := string(routeSource)

	allowlistMounts := strings.Count(source, "groupModelAllowlist)") + strings.Count(source, "groupModelAllowlist,")
	codexMounts := strings.Count(source, "groupCodexCLIOnly)") + strings.Count(source, "groupCodexCLIOnly,")
	require.Equal(t, allowlistMounts, codexMounts, "every chain mounting the model allowlist must also mount the codex gate")

	for _, group := range []string{"gateway", "gemini", "antigravityV1", "antigravityV1Beta"} {
		re := regexp.MustCompile(regexp.QuoteMeta(group+".Use(groupCodexCLIOnly)") + `\s*` + regexp.QuoteMeta(group+".Use(groupModelAllowlist)"))
		require.Regexp(t, re, source, "%s chain must mount groupCodexCLIOnly right before groupModelAllowlist", group)
	}
	require.Contains(t, source, "gin.HandlerFunc(apiKeyAuth), groupCodexCLIOnly, groupModelAllowlist,")
}

func TestGatewayRoutesGroupCodexCLIOnlyRejectsUnofficialClientOnAllEntries(t *testing.T) {
	router := newGatewayRoutesTestRouterWithGroup(codexCLIOnlyGroup(true))

	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/v1/responses", `{"model":"gpt-5.1"}`},
		{http.MethodPost, "/v1/responses/compact", `{"model":"gpt-5.1"}`},
		{http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5.1"}`},
		{http.MethodPost, "/v1/messages", `{"model":"gpt-5.1","messages":[]}`},
		{http.MethodPost, "/v1/messages/count_tokens", `{"model":"gpt-5.1","messages":[]}`},
		{http.MethodPost, "/v1/embeddings", `{"model":"text-embedding-3-small","input":"hi"}`},
		{http.MethodPost, "/v1/images/generations", `{"model":"gpt-image-1"}`},
		{http.MethodGet, "/v1/usage", ""},
		{http.MethodPost, "/responses", `{"model":"gpt-5.1"}`},
		{http.MethodPost, "/chat/completions", `{"model":"gpt-5.1"}`},
		{http.MethodPost, "/backend-api/codex/responses", `{"model":"gpt-5.1"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("User-Agent", "ctrl-probe/1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusForbidden, w.Code, "%s %s should be denied by the codex gate, got body: %s", tc.method, tc.path, w.Body.String())
		require.Contains(t, w.Body.String(), service.CodexGroupOfficialClientsOnlyMessage, "%s %s", tc.method, tc.path)
	}
}

// TestGatewayRoutesGroupCodexCLIOnlyExemptsEveryModelCatalogRoute 遍历实际注册的 GET 模型目录路由，
// 断言分组门都不拦（与账号级一致）；新增同类路由也会被覆盖。handler 依赖为空可能 panic，故加 Recovery。
func TestGatewayRoutesGroupCodexCLIOnlyExemptsEveryModelCatalogRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(gin.Recovery())
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
			AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			groupID := int64(9)
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID, Group: codexCLIOnlyGroup(true)})
			c.Next()
		}),
		nil, nil, nil, nil, nil,
		&config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1024 * 1024, TextMaxBodySize: 1024 * 1024}},
	)

	checked := 0
	for _, route := range router.Routes() {
		isCatalog := strings.HasSuffix(route.Path, "/models") || strings.HasSuffix(route.Path, "/models/:model")
		if route.Method != http.MethodGet || !isCatalog {
			continue
		}
		path := strings.Replace(route.Path, ":model", "gpt-5.1", 1)
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("User-Agent", "ctrl-probe/1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.NotContains(t, w.Body.String(), service.CodexGroupOfficialClientsOnlyMessage, "GET %s must not be blocked by the group codex gate", route.Path)
		checked++
	}
	require.GreaterOrEqual(t, checked, 10, "expected the gateway to register the known model catalog routes")
}

func TestGatewayRoutesGroupCodexCLIOnlyLetsOfficialClientThrough(t *testing.T) {
	router := newGatewayRoutesTestRouterWithGroup(codexCLIOnlyGroup(true))

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex_cli_rs/0.153.4 (Windows 11; x86_64) WindowsTerminal")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("x-codex-window-id", "window-1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.NotEqual(t, http.StatusForbidden, w.Code, "official Codex client must pass the gate, got: %s", w.Body.String())
}

func TestGatewayRoutesGroupCodexCLIOnlyDisabledLeavesRequestsAlone(t *testing.T) {
	router := newGatewayRoutesTestRouterWithGroup(codexCLIOnlyGroup(false))

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ctrl-probe/1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.NotContains(t, w.Body.String(), service.CodexGroupOfficialClientsOnlyMessage)
}

func TestGatewayRoutesGroupCodexCLIOnlySkipsWebSocketUpgrade(t *testing.T) {
	router := newGatewayRoutesTestRouterWithGroup(codexCLIOnlyGroup(true))

	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("User-Agent", "ctrl-probe/1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.NotContains(t, w.Body.String(), service.CodexGroupOfficialClientsOnlyMessage,
		"WS upgrade is checked by ResponsesWebSocket after the first frame")
}
