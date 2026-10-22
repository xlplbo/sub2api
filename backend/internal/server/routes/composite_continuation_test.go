package routes

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCompositeHTTPContinuationRetainsImageRoute(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{routes: []service.CompositeModelRoute{
				{ID: 1, GroupID: 7, PublicModel: "glm-5.3", MatchType: "exact", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "glm-5.3", Endpoint: "responses", Enabled: true},
				{ID: 2, GroupID: 7, PublicModel: "glm-5.3", MatchType: "exact", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "vision-model", Endpoint: "responses", RequestCondition: "has_image", Enabled: true},
			}})
			cacheServer := miniredis.RunT(t)
			cacheClient := redis.NewClient(&redis.Options{Addr: cacheServer.Addr()})
			t.Cleanup(func() { _ = cacheClient.Close() })
			resolver.SetContinuationCache(repository.NewCompositeContinuationCache(cacheClient))
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{UserID: 42, Group: &service.Group{ID: 7, Platform: service.PlatformComposite}})
			})
			var models []string
			router.POST("/v1/responses", compositeTargetPlatformMiddleware(resolver), func(c *gin.Context) {
				model, _ := service.ResolvedUpstreamModelFromContext(c.Request.Context())
				models = append(models, model)
				response := `{"id":"resp_image","object":"response","conversation":{"id":"conv_image"},"status":"completed","output":[]}`
				if stream {
					c.Header("Content-Type", "text/event-stream")
					_, _ = c.Writer.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":")
					c.Writer.Flush()
					_, _ = c.Writer.Write([]byte(response + "}\n\n"))
				} else {
					c.Data(http.StatusOK, "application/json", []byte(response))
				}
			})
			for _, body := range []string{
				`{"model":"glm-5.3","input":[{"content":[{"type":"input_image","image_url":"https://example.org/image.png"}]}]}`,
				`{"model":"glm-5.3","previous_response_id":"resp_image","input":"continue"}`,
				`{"model":"glm-5.3","conversation":{"id":"conv_image"},"input":"continue"}`,
			} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			}
			require.Equal(t, []string{"vision-model", "vision-model", "vision-model"}, models)
		})
	}
}
