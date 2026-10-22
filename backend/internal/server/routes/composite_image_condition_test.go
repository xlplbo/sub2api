package routes

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCompositeImageConditionIngress(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, body string
		image                bool
		status               int
	}{
		{"messages", "/v1/messages", `{"model":"glm-5.3","messages":[{"content":[{"type":"image","source":{"data":"abc"}}]}]}`, true, 204},
		{"count tokens", "/v1/messages/count_tokens", `{"model":"glm-5.3","messages":[{"content":[{"type":"image","source":{"data":"abc"}}]}]}`, true, 204},
		{"chat", "/v1/chat/completions", `{"model":"glm-5.3","messages":[{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]}`, true, 204},
		{"responses", "/v1/responses", `{"model":"glm-5.3","input":[{"content":[{"type":"input_image","file_id":"file-1"}]}]}`, true, 204},
		{"custom tool image", "/v1/responses", `{"model":"glm-5.3","input":[{"type":"custom_tool_call_output","output":{"content":[{"type":"input_image","image_url":"data:image/png;base64,abc"}]}}]}`, true, 204},
		{"text", "/v1/responses", `{"model":"glm-5.3","input":"hello"}`, false, 204},
		{"previous response", "/v1/responses", `{"model":"glm-5.3","previous_response_id":"resp_1","input":"continue"}`, false, 400},
		{"conversation", "/v1/responses", `{"model":"glm-5.3","conversation":{"id":"conv_1"},"input":"continue"}`, false, 400},
		{"gemini", "/v1beta/models/glm-5.3:generateContent", `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"abc"}}]}]}`, true, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{ID: 7, Platform: service.PlatformComposite}})
				c.Next()
			})
			resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{routes: []service.CompositeModelRoute{
				{ID: 1, GroupID: 7, PublicModel: "glm-5.3", MatchType: "exact", Endpoint: "any", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "glm-5.3", Enabled: true},
				{ID: 2, GroupID: 7, PublicModel: "glm-5.3", MatchType: "exact", Endpoint: "any", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "vision-model", RequestCondition: "has_image", Enabled: true},
			}})
			path := tc.endpoint
			mw := compositeTargetPlatformMiddleware(resolver)
			if tc.name == "gemini" {
				path = "/v1beta/models/*modelAction"
				mw = compositeGeminiTargetPlatformMiddleware(resolver)
			}
			r.POST(path, mw, func(c *gin.Context) {
				model, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context())
				require.True(t, ok)
				want := "glm-5.3"
				if tc.image {
					want = "vision-model"
				}
				require.Equal(t, want, model)
				public, ok := service.RequestedPublicModelFromContext(c.Request.Context())
				require.True(t, ok)
				require.Equal(t, "glm-5.3", public)
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				if tc.name == "gemini" {
					require.JSONEq(t, tc.body, string(body))
				} else {
					require.Equal(t, want, gjson.GetBytes(body, "model").String())
				}
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodPost, tc.endpoint, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == 400 {
				require.Contains(t, w.Body.String(), "full history")
			}
		})
	}
}
