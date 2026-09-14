//go:build e2e

package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Opt in with an installed native Codex executable and the existing configured
// provider name. Only that provider's URL and transport are overridden per run.
func TestGatewayCodexWSOnlyCLIStopsAfterHTTP400(t *testing.T) {
	bin := os.Getenv("CODEX_WS_ONLY_E2E_BIN")
	provider := os.Getenv("CODEX_WS_ONLY_E2E_PROVIDER")
	if bin == "" || provider == "" {
		t.Skip("set CODEX_WS_ONLY_E2E_BIN and CODEX_WS_ONLY_E2E_PROVIDER to run the installed CLI with its global configuration")
	}
	for _, transport := range []string{"http", "ws_fallback"} {
		t.Run(transport, func(t *testing.T) {
			var posts, upgrades atomic.Int32
			router := newGatewayRoutesTestRouterWithGroup(&service.Group{Platform: service.PlatformOpenAI, CodexWSOnly: true})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/v1/responses" {
					upgrades.Add(1)
					// Exercise native Codex WS -> HTTP fallback without an upstream account.
					w.WriteHeader(http.StatusUpgradeRequired)
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/v1/responses" {
					posts.Add(1)
				}
				router.ServeHTTP(w, r)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			ws := "false"
			if transport == "ws_fallback" {
				ws = "true"
			}
			cmd := exec.CommandContext(ctx, bin, "exec", "--json", "--skip-git-repo-check",
				"-c", "model_providers."+provider+`.base_url="`+server.URL+`/v1"`,
				"-c", "model_providers."+provider+".supports_websockets="+ws,
				"Reply only OK.")
			cmd.WaitDelay = 5 * time.Second
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err(), "installed CLI did not finish before the timeout")
			require.Error(t, err, "the policy rejection must fail the CLI turn")
			policyError := strings.Contains(string(output), "codex_websocket_required")
			t.Logf("transport=%s HTTP POST=%d WS GET=%d policy_error=%t exit=%v", transport, posts.Load(), upgrades.Load(), policyError, err)
			require.True(t, policyError, "CLI must surface the gateway policy error")
			require.Equal(t, int32(1), posts.Load(), "HTTP 400 must not be retried")
			if transport == "ws_fallback" {
				require.Positive(t, upgrades.Load(), "the test must actually attempt WS before HTTP")
			} else {
				require.Zero(t, upgrades.Load())
			}
		})
	}
}
