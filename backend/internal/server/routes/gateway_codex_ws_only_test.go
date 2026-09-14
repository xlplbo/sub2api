package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayRoutesCodexWSOnlyRejectsBeforeDispatch(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformComposite} {
		for _, path := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses", "/responses/"} {
			t.Run(platform+path, func(t *testing.T) {
				router := newGatewayRoutesTestRouterWithGroup(&service.Group{Platform: platform, CodexWSOnly: true})
				req := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", "codex_cli_rs/0.153.4")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				// These handlers/resolver have no dependencies: reaching them would fail differently.
				require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), `"code":"codex_websocket_required"`)
			})
		}
	}
}

func TestGatewayRoutesCodexWSOnlyLegacyCompactionFollowsHandler(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses"} {
		for _, tt := range []struct {
			platform, model string
			blocked         bool
		}{
			{service.PlatformOpenAI, "gpt-5.4", false},
			{service.PlatformComposite, "gpt-5.4", false},
			{service.PlatformComposite, "claude-sonnet-4-5", true},
		} {
			t.Run(path+tt.platform+tt.model, func(t *testing.T) {
				router := newGatewayRoutesTestRouterWithGroup(&service.Group{Platform: tt.platform, CodexWSOnly: true})
				body := `{"model":"` + tt.model + `","input":[{"role":"user","content":"hello"},{"type":"compaction_trigger"}],"stream":false}`
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", "codex_cli_rs/0.153.4")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if tt.blocked {
					require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
					require.Contains(t, w.Body.String(), `"code":"codex_websocket_required"`)
				} else {
					require.NotContains(t, w.Body.String(), "codex_websocket_required")
					require.Contains(t, w.Body.String(), "User context not found", "legacy compact must reach its original handler")
				}
			})
		}
	}
}
