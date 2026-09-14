package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGroupCodexWSOnlyAdmission(t *testing.T) {
	const generate = `{"model":"gpt-5.4","input":"hello","stream":true}`
	const legacyCompact = `{"model":"gpt-5.4","input":[{"type":"compaction_trigger"}]}`
	const nativeCompact = `{"model":"gpt-5.4","input":[{"type":"compaction_trigger"}],"stream":true}`
	for _, tt := range []struct {
		name, method, path, ua, originator, body string
		enabled, blocked                         bool
	}{
		{"disabled", "POST", "/v1/responses", "codex_cli_rs/0.153.4", "", generate, false, false},
		{"direct HTTP", "POST", "/v1/responses", "codex_cli_rs/0.153.4", "", generate, true, true},
		{"fallback HTTP", "POST", "/v1/responses", "codex-tui/0.153.4", "codex-tui", generate, true, true},
		{"root alias", "POST", "/responses", "codex_exec/0.153.4", "", generate, true, true},
		{"backend alias", "POST", "/backend-api/codex/responses", "codex_app/0.153.4", "", generate, true, true},
		{"openai path", "POST", "/openai/v1/responses", "codex_vscode/0.153.4", "", generate, true, true},
		{"trailing slash", "POST", "/v1/responses/", "codex_cli_rs/0.153.4", "", generate, true, true},
		{"query string", "POST", "/responses?client_version=0.153.4", "codex_cli_rs/0.153.4", "", generate, true, true},
		{"desktop", "POST", "/responses", "Codex Desktop/0.153.4", "", generate, true, true},
		{"originator", "POST", "/responses", "", "codex_cli_rs", generate, true, true},
		{"overridden originator", "POST", "/responses", "cccc/0.153.4 (Windows; x86_64) (codex-tui; 0.153.4)", "cccc", generate, true, true},
		{"other client", "POST", "/responses", "curl/8", "", generate, true, false},
		{"no identity", "POST", "/responses", "", "", generate, true, false},
		{"unrelated UA token", "POST", "/responses", "Mozilla/5.0 codex_cli_rs/0.153.4", "", generate, true, false},
		{"compact", "POST", "/responses/compact", "codex_cli_rs/0.153.4", "", generate, true, false},
		{"input tokens", "POST", "/responses/input_tokens", "codex_cli_rs/0.153.4", "", generate, true, false},
		{"search", "POST", "/backend-api/codex/alpha/search", "codex_cli_rs/0.153.4", "", generate, true, false},
		{"models", "GET", "/v1/models", "codex_cli_rs/0.153.4", "", "", true, false},
		{"WS route", "GET", "/v1/responses", "codex_cli_rs/0.153.4", "", "", true, false},
		{"legacy body signal", "POST", "/responses", "codex_cli_rs/0.153.4", "", legacyCompact, true, false},
		{"native compaction v2", "POST", "/responses", "codex_cli_rs/0.153.4", "", nativeCompact, true, true},
		{"nonstream generation", "POST", "/responses", "codex_cli_rs/0.153.4", "", `{"input":"hello","stream":false}`, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			var reason IngressRejectReason
			var called bool
			router.Use(func(c *gin.Context) {
				c.Set(string(ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, CodexWSOnly: tt.enabled}})
				c.Next()
				reason, _ = GetIngressRejectReason(c)
			})
			router.Use(GroupCodexWSOnly())
			path := strings.SplitN(tt.path, "?", 2)[0]
			router.Handle(tt.method, path, func(c *gin.Context) {
				called = true
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Equal(t, tt.body, string(body))
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("User-Agent", tt.ua)
			req.Header.Set("originator", tt.originator)
			// A forged Upgrade header on POST must never bypass HTTP admission.
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Connection", "Upgrade")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, !tt.blocked, called)
			if tt.blocked {
				require.Equal(t, http.StatusBadRequest, w.Code)
				require.JSONEq(t, `{"error":{"type":"invalid_request_error","code":"codex_websocket_required","message":"This group requires Codex to connect via WebSocket. Enable WebSocket and start a new client session."}}`, w.Body.String())
				require.Equal(t, IngressRejectCodexWebSocketRequired, reason)
			} else {
				require.Equal(t, http.StatusNoContent, w.Code)
				require.Empty(t, reason)
			}
		})
	}
}

func TestGroupCodexWSOnlySkipsUnboundAndUnsupportedGroupsWithoutReading(t *testing.T) {
	for _, key := range []*service.APIKey{
		nil, {}, {Group: &service.Group{}},
		{Group: &service.Group{Platform: service.PlatformAnthropic, CodexWSOnly: true}},
	} {
		gin.SetMode(gin.TestMode)
		router := gin.New()
		router.Use(func(c *gin.Context) { c.Set(string(ContextKeyAPIKey), key) })
		router.Use(GroupCodexWSOnly())
		router.POST("/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		body := &readTrackingBody{Reader: strings.NewReader(`{"input":"hello"}`)}
		req := httptest.NewRequest("POST", "/responses", body)
		req.Header.Set("User-Agent", "codex_cli_rs/0.153.4")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNoContent, w.Code)
		require.False(t, body.read)
	}
}

func TestGroupCodexWSOnlyBodyHandling(t *testing.T) {
	legacy := []byte(`{"input":[{"type":"compaction_trigger"}],"stream":false}`)
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err := gz.Write(legacy)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	for _, tt := range []struct {
		name     string
		body     io.Reader
		encoding string
		limit    int64
		want     int
	}{
		{"gzip compact", bytes.NewReader(compressed.Bytes()), "gzip", 4096, 204},
		{"preread compact", httputil.NewPrereadBody(legacy), "", 4096, 204},
		{"too large", strings.NewReader(`{"input":"hello"}`), "", 4, 413},
		{"read error", codexWSOnlyBrokenBody{}, "", 4096, 400},
		{"broken gzip", strings.NewReader("broken"), "gzip", 4096, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequestBodyLimit(tt.limit), func(c *gin.Context) {
				c.Set(string(ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, CodexWSOnly: true}})
			}, GroupCodexWSOnly())
			called := false
			router.POST("/responses", func(c *gin.Context) {
				called = true
				body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
				require.NoError(t, err)
				require.Equal(t, legacy, body)
				require.Empty(t, c.GetHeader("Content-Encoding"))
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest("POST", "/responses", tt.body)
			req.Header.Set("User-Agent", "codex_cli_rs/0.153.4")
			req.Header.Set("Content-Encoding", tt.encoding)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tt.want, w.Code)
			require.Equal(t, tt.want == http.StatusNoContent, called)
		})
	}
}

type codexWSOnlyBrokenBody struct{}

func (codexWSOnlyBrokenBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestGroupCodexWSOnlyAllowsWebSocketMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, CodexWSOnly: true}})
	}, GroupCodexWSOnly())
	router.GET("/responses", func(c *gin.Context) {
		conn, err := websocket.Accept(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		typ, body, err := conn.Read(c.Request.Context())
		if err == nil {
			_ = conn.Write(c.Request.Context(), typ, body)
		}
	})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/responses", &websocket.DialOptions{
		HTTPHeader: http.Header{"User-Agent": {"codex_cli_rs/0.153.4"}},
	})
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	body := []byte(`{"type":"response.create","model":"gpt-5.4"}`)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, body))
	_, echoed, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, body, echoed)
}
