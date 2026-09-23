package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// CodexGroupOfficialClientsOnlyMessage 是分组级 codex_cli_only 拒绝时的通用兜底文案，
// 取舍同 CodexOfficialClientsOnlyMessage（只有版本越界给差异化提示）。
const CodexGroupOfficialClientsOnlyMessage = "This group only allows Codex official clients"

// CodexCLIOnlyEnabled 报告分组是否要求全部网关请求来自 Codex 官方客户端（仅 openai 平台生效）。
func (g *Group) CodexCLIOnlyEnabled() bool {
	return g != nil && g.Platform == PlatformOpenAI && g.CodexCLIOnly
}

// DetectGroupCodexClientRestriction 按分组级 codex_cli_only 判定请求客户端，
// 判定规则与账号级一致，App Server 开闸取全局开关 OR 分组开关。
func DetectGroupCodexClientRestriction(c *gin.Context, group *Group, policy CodexRestrictionPolicy, forceCodexCLI bool, body []byte) CodexClientRestrictionDetectionResult {
	if !group.CodexCLIOnlyEnabled() {
		return CodexClientRestrictionDetectionResult{Enabled: false, Matched: false, Reason: CodexClientRestrictionReasonDisabled}
	}
	return detectCodexOfficialClient(c, policy, body, forceCodexCLI, group.CodexCLIOnlyAllowAppServer)
}

// CheckGroupCodexClientRestriction 以网关的全局策略与 force_codex_cli 配置执行分组级判定，
// 供 handler 内的入口（如 Responses WebSocket 首帧）使用。
func (s *OpenAIGatewayService) CheckGroupCodexClientRestriction(c *gin.Context, group *Group, body []byte) CodexClientRestrictionDetectionResult {
	if !group.CodexCLIOnlyEnabled() {
		return CodexClientRestrictionDetectionResult{Enabled: false, Matched: false, Reason: CodexClientRestrictionReasonDisabled}
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	var settingService *SettingService
	forceCodexCLI := false
	if s != nil {
		settingService = s.settingService
		forceCodexCLI = s.cfg != nil && s.cfg.Gateway.ForceCodexCLI
	}
	return DetectGroupCodexClientRestriction(c, group, CodexRestrictionPolicyOrDefault(ctx, settingService), forceCodexCLI, body)
}

// CodexGroupClientRestrictionMessage 把分组级判定结果映射为面向客户端的拒绝文案。
func CodexGroupClientRestrictionMessage(r CodexClientRestrictionDetectionResult) string {
	switch r.Reason {
	case CodexClientRestrictionReasonVersionTooLow, CodexClientRestrictionReasonVersionTooHigh:
		return CodexClientRestrictionMessage(r)
	default:
		return CodexGroupOfficialClientsOnlyMessage
	}
}

// CodexRestrictionPolicyOrDefault 读取 codex_cli_only 全局策略；settingService 缺失时
// 指纹门保持默认种子，避免零值策略让判定失败开放。
func CodexRestrictionPolicyOrDefault(ctx context.Context, settingService *SettingService) CodexRestrictionPolicy {
	if settingService == nil {
		return CodexRestrictionPolicy{EngineFingerprintSignals: openai.DefaultEngineFingerprintSignals}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return settingService.GetCodexRestrictionPolicy(ctx)
}

// LogGroupCodexCLIOnlyRejection 记录分组级 codex_cli_only 拒绝（带请求诊断字段）。
// 分组门对该分组每个请求都会判定，放行不记日志，避免刷屏。
func LogGroupCodexCLIOnlyRejection(ctx context.Context, c *gin.Context, groupID, apiKeyID int64, result CodexClientRestrictionDetectionResult, body []byte) {
	if !result.Enabled || result.Matched {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fields := []zap.Field{
		zap.String("component", "service.group_codex_cli_only"),
		zap.Int64("group_id", groupID),
		zap.String("reject_reason", result.Reason),
	}
	if apiKeyID > 0 {
		fields = append(fields, zap.Int64("api_key_id", apiKeyID))
	}
	fields = appendCodexCLIOnlyRejectedRequestFields(fields, c, body)
	logger.FromContext(ctx).With(fields...).Warn("OpenAI 分组 codex_cli_only 拒绝非官方客户端请求")
}
