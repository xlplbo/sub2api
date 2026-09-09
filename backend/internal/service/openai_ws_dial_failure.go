package service

import (
	"context"
	"net/http"
)

// handleOpenAIWSDialTransportFailure 处理 WS 拨号阶段的传输层失败（未收到 HTTP 状态码）：
// 当次请求返回可换号的错误交给 handler 切账号，封禁与否由 HTTP 路径共用的传输失败策略决定。
// 收到 HTTP 状态码或客户端已取消时返回 nil，由调用方沿用既有分支。
func (s *OpenAIGatewayService) handleOpenAIWSDialTransportFailure(ctx context.Context, account *Account, turn int, dialErr *openAIWSDialError) *UpstreamFailoverError {
	if s == nil || account == nil || dialErr == nil || dialErr.StatusCode != 0 || ctx.Err() != nil {
		return nil
	}
	safeErr := sanitizeUpstreamErrorMessage(dialErr.Error())
	failures, blocked := s.applyOpenAITransportFailurePolicy(ctx, account, safeErr, classifyOpenAITransportFailure(dialErr.Err, true))
	logOpenAIWSModeInfo(
		"ingress_ws_dial_transport_failover account_id=%d turn=%d failures_in_window=%d blocked=%v cause=%s",
		account.ID,
		turn,
		failures,
		blocked,
		truncateOpenAIWSLogValue(safeErr, openAIWSLogValueMaxLen),
	)
	return s.newOpenAIAccountFailoverError(account, http.StatusBadGateway, nil, openAITransportFailoverBody, safeErr, false, false)
}
