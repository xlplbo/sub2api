//go:build integration

package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestCompositeImageRouteCodexE2E runs the real middleware and Codex CLI against
// an opt-in gateway upstream. It preserves the caller's global Codex config.
func TestCompositeImageRouteCodexE2E(t *testing.T) {
	upstream := os.Getenv("SUB2API_IMAGE_E2E_URL")
	if upstream == "" {
		t.Skip("set SUB2API_IMAGE_E2E_URL and related variables for a real client smoke test")
	}
	key := os.Getenv("SUB2API_IMAGE_E2E_KEY")
	entry := os.Getenv("SUB2API_IMAGE_E2E_CODEX_ENTRY")
	picture := os.Getenv("SUB2API_IMAGE_E2E_PICTURE")
	expected := os.Getenv("SUB2API_IMAGE_E2E_EXPECTED")
	publicModel := os.Getenv("SUB2API_IMAGE_E2E_PUBLIC_MODEL")
	visionModel := os.Getenv("SUB2API_IMAGE_E2E_VISION_MODEL")
	for name, value := range map[string]string{
		"key": key, "codex entry": entry, "picture": picture, "expected": expected,
		"public model": publicModel, "vision model": visionModel,
	} {
		require.NotEmpty(t, value, "%s must be configured", name)
	}
	target, err := url.Parse(upstream)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{routes: []service.CompositeModelRoute{
		{ID: 1, GroupID: 7, PublicModel: publicModel, MatchType: "exact", Endpoint: "responses", TargetPlatform: service.PlatformOpenAI, UpstreamModel: publicModel, Enabled: true},
		{ID: 2, GroupID: 7, PublicModel: publicModel, MatchType: "exact", Endpoint: "responses", TargetPlatform: service.PlatformOpenAI, UpstreamModel: visionModel, RequestCondition: "has_image", Enabled: true},
	}})
	var mu sync.Mutex
	var models []string
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{ID: 7, Platform: service.PlatformComposite}})
		c.Next()
	})
	r.Any("/*path", compositeTargetPlatformMiddleware(resolver), func(c *gin.Context) {
		if model, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context()); ok {
			mu.Lock()
			models = append(models, model)
			mu.Unlock()
		}
		proxy.ServeHTTP(c.Writer, c.Request)
	})
	server := httptest.NewServer(r)
	defer server.Close()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "route-marker.txt"), []byte("composite-image-routing-ok"), 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", entry,
		"exec", "--skip-git-repo-check", "--ephemeral", "-C", dir, "--model", publicModel,
		"-c", `model_provider="custom"`,
		"-c", `model_reasoning_effort="low"`,
		"-c", `model_providers.custom.base_url="`+server.URL+`/v1"`,
		"-c", `model_providers.custom.env_key="SUB2API_IMAGE_E2E_KEY"`,
		"-c", `model_providers.custom.supports_websockets=false`,
		"-c", `model_providers.custom.requires_openai_auth=false`,
		"--image", picture, "--json",
		"Read the six-digit number in the attached image. Then use a shell command to read route-marker.txt in the current directory. Report both the number and the file content.")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", strings.ReplaceAll(string(output), key, "[redacted]"))
	require.Contains(t, string(output), expected)
	require.Contains(t, string(output), "composite-image-routing-ok")
	require.Contains(t, string(output), "command_execution")
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(models), 2, "exercise the image request and a tool-result continuation")
	for _, model := range models {
		require.Equal(t, visionModel, model)
	}
	t.Logf("Codex read the image and marker; %d requests routed from %s to %s", len(models), publicModel, visionModel)
}
