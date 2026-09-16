package handler

import "github.com/Wei-Shaw/sub2api/internal/service"

// openAIWSFirstAdmissionNonMigratable 判定连接首次准入是否把透传续聊帧搬到了活绑定之外的账号。
// 透传不解析帧，换账号后上下文无法恢复；ctx_pool 与 http_bridge 能重建上下文，允许迁移。
// 判据取连接建立时的原始首帧，不用 waitBudget.continuation：后续轮 failover 也会把它置真，
// 而那时载荷已由转发器重建成完整输入，可以安全换号；failover 重选一律不判（switchCount > 0）。
func openAIWSFirstAdmissionNonMigratable(switchCount int, firstFrameContinuation bool, admissionMode string, decision service.OpenAIAccountScheduleDecision, selectedAccountID int64, lookupBound func() int64) bool {
	if switchCount > 0 || !firstFrameContinuation || admissionMode != service.OpenAIWSIngressModePassthrough {
		return false
	}
	if decision.StickySessionHit || decision.StickyPreviousHit {
		return false
	}
	bound := lookupBound()
	return bound > 0 && bound != selectedAccountID
}
