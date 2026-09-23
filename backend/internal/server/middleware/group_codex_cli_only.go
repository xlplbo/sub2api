package middleware

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// GroupCodexCLIOnly 是分组级「仅允许 Codex 官方客户端」准入中间件。
//
// 挂载位置：每条网关链的 apiKeyAuth 之后，与 GroupModelAllowlist 同链，覆盖分组全部网关入口。
//
// 行为：
//   - 快速路径：未绑定分组或分组未开启（仅 openai 平台生效）时直接放行，不读请求体。
//   - Responses WebSocket 入口跳过：引擎指纹的 body 信号在首帧里，由 ResponsesWebSocket
//     读到首帧后判定。
//   - 模型目录（GET .../models、.../models/:model）跳过：只返回列表、不经账号转发，与账号级
//     codex_cli_only 一致不限制；官方 Codex 的模型目录请求不带 x-codex-* 头，套用指纹门会误拦。
//   - 带请求体的方法读体供引擎指纹的 body 信号使用，读完回填（PrereadBody）。
//   - 判定规则与账号级 codex_cli_only 相同（全局黑白名单、版本区间、引擎指纹门）。
//   - 拒绝：按入口协议格式返回 403，并标记运维业务限流原因 local_policy_denied
//     与 ingress 拒绝原因 codex_client_restricted。
func GroupCodexCLIOnly(settingService *service.SettingService, cfg *config.Config) gin.HandlerFunc {
	forceCodexCLI := cfg != nil && cfg.Gateway.ForceCodexCLI
	return func(c *gin.Context) {
		apiKey, ok := GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || !apiKey.Group.CodexCLIOnlyEnabled() || c.Request == nil {
			c.Next()
			return
		}
		if isResponsesWebSocketRoute(c) || isModelCatalogRoute(c) {
			c.Next()
			return
		}

		var body []byte
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			read, done := prereadGatewayRequestBody(c)
			if !done {
				return
			}
			body = read
		}

		ctx := c.Request.Context()
		policy := service.CodexRestrictionPolicyOrDefault(ctx, settingService)
		result := service.DetectGroupCodexClientRestriction(c, apiKey.Group, policy, forceCodexCLI, body)
		if !result.Enabled || result.Matched {
			c.Next()
			return
		}

		service.LogGroupCodexCLIOnlyRejection(ctx, c, apiKey.Group.ID, apiKey.ID, result, body)
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		MarkIngressRejected(c, IngressRejectCodexClientRestricted)
		gatewayProtocolErrorWriter(c, openAIForbiddenErrorWriter)(c, http.StatusForbidden, service.CodexGroupClientRestrictionMessage(result))
		c.Abort()
	}
}

// isModelCatalogRoute 判断是否为模型目录入口：GET 且路由以 /models 或 /models/:model 结尾。
// Gemini 的 POST /models/*modelAction 是生成请求，不在此列。
func isModelCatalogRoute(c *gin.Context) bool {
	if c.Request == nil || c.Request.Method != http.MethodGet {
		return false
	}
	route := c.FullPath()
	return strings.HasSuffix(route, "/models") || strings.HasSuffix(route, "/models/:model")
}

// openAIForbiddenErrorWriter 按 OpenAI 兼容入口输出 403，格式同账号级 codex_cli_only 拒绝。
func openAIForbiddenErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    "forbidden_error",
			"message": message,
		},
	})
}
