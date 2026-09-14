package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const codexWSOnlyLegacyCompactKey = "codex_ws_only_legacy_compact"

// GroupCodexWSOnly runs after authentication and before composite routing.
func GroupCodexWSOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey, ok := GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || apiKey.Group == nil || !apiKey.Group.CodexWSOnly ||
			(apiKey.Group.Platform != service.PlatformOpenAI && apiKey.Group.Platform != service.PlatformComposite) ||
			c.Request == nil || c.Request.Method != http.MethodPost || !isCodexHTTPResponsesPath(c.Request.URL.Path) ||
			(!openai.IsCodexOfficialClientRequestStrict(c.GetHeader("User-Agent")) && !openai.IsCodexOfficialClientOriginator(c.GetHeader("originator"))) {
			c.Next()
			return
		}

		body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			status, message := http.StatusBadRequest, "Failed to read request body"
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				status, message = http.StatusRequestEntityTooLarge, "Request body is too large"
			}
			c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
			return
		}
		requestmodel.ResetRequestBody(c.Request, body)
		// Match normalizeOpenAIResponsesCompactRequest: only stream:true keeps
		// compaction_trigger on the native Responses wire; legacy compact is auxiliary.
		if service.HasCompactionTriggerInInput(body) && gjson.GetBytes(body, "stream").Type != gjson.True {
			c.Set(codexWSOnlyLegacyCompactKey, true)
			c.Next()
			return
		}

		rejectCodexWebSocketRequired(c)
	}
}

// RejectCodexWSOnlyLegacyGeneration closes the legacy compact exemption when
// routing selects a handler that would treat the body as ordinary generation.
func RejectCodexWSOnlyLegacyGeneration(c *gin.Context) bool {
	if !c.GetBool(codexWSOnlyLegacyCompactKey) {
		return false
	}
	rejectCodexWebSocketRequired(c)
	return true
}

func rejectCodexWebSocketRequired(c *gin.Context) {
	MarkIngressRejected(c, IngressRejectCodexWebSocketRequired)
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"type":    "invalid_request_error",
		"code":    "codex_websocket_required",
		"message": "This group requires Codex to connect via WebSocket. Enable WebSocket and start a new client session.",
	}})
}

func isCodexHTTPResponsesPath(path string) bool {
	switch strings.TrimRight(path, "/") {
	case "/v1/responses", "/responses", "/backend-api/codex/responses", "/openai/v1/responses":
		return true
	default:
		return false
	}
}
