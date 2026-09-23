package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

var (
	groupCodexOfficialHeaders = map[string]string{
		"User-Agent":        "codex_cli_rs/0.153.4 (Windows 11; x86_64) WindowsTerminal",
		"originator":        "codex_cli_rs",
		"x-codex-window-id": "window-1",
	}
	groupCodexAppServerHeaders = map[string]string{
		"User-Agent":        "claude_probe/0.153.4 (Windows 10.0.26200; x86_64) unknown (claude_probe; 0.1)",
		"originator":        "claude_probe",
		"x-codex-window-id": "window-2",
	}
	groupCodexScriptHeaders = map[string]string{"User-Agent": "ctrl-probe/1", "session_id": "sess-1"}
)

func codexCLIOnlyAPIKey(platform string, enabled, allowAppServer bool) *service.APIKey {
	return &service.APIKey{
		ID: 21,
		Group: &service.Group{
			ID: 9, Platform: platform, CodexCLIOnly: enabled, CodexCLIOnlyAllowAppServer: allowAppServer,
		},
	}
}

func newGroupCodexCLIOnlyTestRouter(apiKey *service.APIKey) (*gin.Engine, *[]string) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var calls []string
	router.Use(func(c *gin.Context) {
		if apiKey != nil {
			c.Set(string(ContextKeyAPIKey), apiKey)
		}
		c.Next()
	})
	router.Use(GroupCodexCLIOnly(nil, nil))
	handler := func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		calls = append(calls, c.FullPath()+"|"+string(body))
		c.Status(http.StatusOK)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/v1/responses"},
		{http.MethodGet, "/v1/responses"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodGet, "/v1/models"},
		{http.MethodGet, "/v1/models/:model"},
		{http.MethodGet, "/v1/images/batches/models"},
		{http.MethodGet, "/backend-api/codex/models"},
		{http.MethodGet, "/v1beta/models/:model"},
		{http.MethodPost, "/v1beta/models/*modelAction"},
	} {
		router.Handle(route.method, route.path, handler)
	}
	return router, &calls
}

func serveGroupCodexCLIOnly(router *gin.Engine, method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestGroupCodexCLIOnlyDisabledDoesNotReadBody(t *testing.T) {
	for _, apiKey := range []*service.APIKey{
		nil,
		{Group: nil},
		codexCLIOnlyAPIKey(service.PlatformOpenAI, false, false),
		codexCLIOnlyAPIKey(service.PlatformAnthropic, true, false),
	} {
		gin.SetMode(gin.TestMode)
		router := gin.New()
		router.Use(func(c *gin.Context) {
			if apiKey != nil {
				c.Set(string(ContextKeyAPIKey), apiKey)
			}
			c.Next()
		})
		router.Use(GroupCodexCLIOnly(nil, nil))
		router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusOK) })

		body := &readTrackingBody{Reader: strings.NewReader(`{"model":"gpt-5.1"}`)}
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
		for k, v := range groupCodexScriptHeaders {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		require.False(t, body.read, "未开启时快速路径不得读取请求体")
	}
}

func TestGroupCodexCLIOnlyRejectsUnofficialClientPerProtocol(t *testing.T) {
	router, calls := newGroupCodexCLIOnlyTestRouter(codexCLIOnlyAPIKey(service.PlatformOpenAI, true, false))

	for _, tc := range []struct {
		name, method, path string
		assertBody         func(t *testing.T, body map[string]any)
	}{
		{name: "openai responses", method: http.MethodPost, path: "/v1/responses", assertBody: assertOpenAIForbiddenBody},
		{name: "openai chat completions", method: http.MethodPost, path: "/v1/chat/completions", assertBody: assertOpenAIForbiddenBody},
		{name: "anthropic messages", method: http.MethodPost, path: "/v1/messages", assertBody: func(t *testing.T, body map[string]any) {
			require.Equal(t, "error", body["type"])
			errBody, ok := body["error"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "permission_error", errBody["type"])
			require.Equal(t, service.CodexGroupOfficialClientsOnlyMessage, errBody["message"])
		}},
		{name: "gemini native", method: http.MethodPost, path: "/v1beta/models/gemini-2.5-pro:generateContent", assertBody: func(t *testing.T, body map[string]any) {
			errBody, ok := body["error"].(map[string]any)
			require.True(t, ok)
			require.EqualValues(t, http.StatusForbidden, errBody["code"])
			require.Equal(t, service.CodexGroupOfficialClientsOnlyMessage, errBody["message"])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serveGroupCodexCLIOnly(router, tc.method, tc.path, `{"model":"gpt-5.1"}`, groupCodexScriptHeaders)
			require.Equal(t, http.StatusForbidden, w.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			tc.assertBody(t, body)
		})
	}
	require.Empty(t, *calls, "被拒请求不得进入 handler")
}

func assertOpenAIForbiddenBody(t *testing.T, body map[string]any) {
	t.Helper()
	errBody, ok := body["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "forbidden_error", errBody["type"])
	require.Equal(t, service.CodexGroupOfficialClientsOnlyMessage, errBody["message"])
}

func TestGroupCodexCLIOnlyMarksIngressReject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var reason IngressRejectReason
	var rejected bool
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), codexCLIOnlyAPIKey(service.PlatformOpenAI, true, false))
		c.Next()
		reason, rejected = GetIngressRejectReason(c)
	})
	router.Use(GroupCodexCLIOnly(nil, nil))
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := serveGroupCodexCLIOnly(router, http.MethodPost, "/v1/responses", `{}`, groupCodexScriptHeaders)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.True(t, rejected)
	require.Equal(t, IngressRejectCodexClientRestricted, reason)
}

func TestGroupCodexCLIOnlyAllowsOfficialClientWithIntactBody(t *testing.T) {
	router, calls := newGroupCodexCLIOnlyTestRouter(codexCLIOnlyAPIKey(service.PlatformOpenAI, true, false))

	w := serveGroupCodexCLIOnly(router, http.MethodPost, "/v1/responses", `{"model":"gpt-5.1","input":"hi"}`, groupCodexOfficialHeaders)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{`/v1/responses|{"model":"gpt-5.1","input":"hi"}`}, *calls)
}

func TestGroupCodexCLIOnlyAppServerClientNeedsGroupSwitch(t *testing.T) {
	denied, _ := newGroupCodexCLIOnlyTestRouter(codexCLIOnlyAPIKey(service.PlatformOpenAI, true, false))
	require.Equal(t, http.StatusForbidden, serveGroupCodexCLIOnly(denied, http.MethodPost, "/v1/responses", `{}`, groupCodexAppServerHeaders).Code)

	allowed, calls := newGroupCodexCLIOnlyTestRouter(codexCLIOnlyAPIKey(service.PlatformOpenAI, true, true))
	require.Equal(t, http.StatusOK, serveGroupCodexCLIOnly(allowed, http.MethodPost, "/v1/responses", `{}`, groupCodexAppServerHeaders).Code)
	require.Len(t, *calls, 1)
}

// 模型目录只返回列表、不经账号转发，与账号级 codex_cli_only 一致不做客户端限制；
// 官方 Codex 的模型目录请求不带 x-codex-* 头，套用对话指纹规则会误拦。
func TestGroupCodexCLIOnlySkipsModelCatalogRoutes(t *testing.T) {
	router, calls := newGroupCodexCLIOnlyTestRouter(codexCLIOnlyAPIKey(service.PlatformOpenAI, true, false))
	officialCatalogHeaders := map[string]string{
		"User-Agent": groupCodexOfficialHeaders["User-Agent"],
		"originator": groupCodexOfficialHeaders["originator"],
		"Version":    "0.153.4",
	}

	for _, tc := range []struct {
		path   string
		header map[string]string
	}{
		{"/v1/models?client_version=0.153.4", officialCatalogHeaders},
		{"/backend-api/codex/models?client_version=0.153.4", officialCatalogHeaders},
		{"/v1/models", groupCodexScriptHeaders},
		{"/v1/models/gpt-5.1", groupCodexScriptHeaders},
		{"/v1/images/batches/models", groupCodexScriptHeaders},
		{"/v1beta/models/gemini-2.5-pro", groupCodexScriptHeaders},
	} {
		w := serveGroupCodexCLIOnly(router, http.MethodGet, tc.path, "", tc.header)
		require.Equal(t, http.StatusOK, w.Code, "GET %s must bypass the group codex gate, got: %s", tc.path, w.Body.String())
	}
	require.Len(t, *calls, 6)

	generate := serveGroupCodexCLIOnly(router, http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", `{}`, groupCodexScriptHeaders)
	require.Equal(t, http.StatusForbidden, generate.Code, "POST /models/*modelAction is generation, not a catalog")
}

func TestGroupCodexCLIOnlySkipsResponsesWebSocketRoute(t *testing.T) {
	router, calls := newGroupCodexCLIOnlyTestRouter(codexCLIOnlyAPIKey(service.PlatformOpenAI, true, false))

	w := serveGroupCodexCLIOnly(router, http.MethodGet, "/v1/responses", "", groupCodexScriptHeaders)

	require.Equal(t, http.StatusOK, w.Code, "Responses WS 由 ResponsesWebSocket 在首帧后判定")
	require.Len(t, *calls, 1)
}
