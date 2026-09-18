package service

import (
	"context"
	"time"

	"github.com/tidwall/gjson"
)

func (s *OpenAIGatewayService) resolveOpenAIWSIngressMode(account *Account) string {
	if account.Platform == PlatformGrok || (s.pluginManager != nil && s.pluginManager.ShouldRouteOpenAIOAuth(account)) {
		return OpenAIWSIngressModeHTTPBridge
	}
	if s.cfg != nil && s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled {
		return account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault)
	}
	return OpenAIWSIngressModeCtxPool
}

// ResolveOpenAIWSAccountAdmissionMode resolves the pre-dial admission mode.
// The forwarder reports the final mode again after payload normalization.
func (s *OpenAIGatewayService) ResolveOpenAIWSAccountAdmissionMode(account *Account, payload []byte) string {
	mode := s.resolveOpenAIWSIngressMode(account)
	if mode == OpenAIWSIngressModePassthrough {
		if s.shouldBridgeOpenAIWSPassthroughFirstMessage(account, payload) {
			return OpenAIWSIngressModeHTTPBridge
		}
	} else if mode != OpenAIWSIngressModeOff && s.shouldBridgeOpenAIWSHTTP(account, len(payload), gjson.GetBytes(payload, "previous_response_id").String()) {
		return OpenAIWSIngressModeHTTPBridge
	}
	return mode
}

// OpenAIAdmissionOptions 由入口在调用调度器前给出，随 ctx 进入调度请求。
type OpenAIAdmissionOptions struct {
	// ContinuationEligible 为真表示会话哈希来自真实会话标识：命中粘性或 previous_response 时
	// 计划标续聊类并参与账号连续准入计数。回退种子哈希的连接置假，计划标新会话类。
	ContinuationEligible bool
	// StickyFullWaits 为真时高级调度对粘性账号满槽不逃逸，改返回粘性等待计划。只有 WS 入口置位。
	StickyFullWaits bool
}

type openAIAdmissionOptionsContextKey struct{}

func WithOpenAIAdmissionOptions(ctx context.Context, opts OpenAIAdmissionOptions) context.Context {
	return context.WithValue(ctx, openAIAdmissionOptionsContextKey{}, opts)
}

func openAIAdmissionOptionsFromContext(ctx context.Context) OpenAIAdmissionOptions {
	if ctx != nil {
		if opts, ok := ctx.Value(openAIAdmissionOptionsContextKey{}).(OpenAIAdmissionOptions); ok {
			return opts
		}
	}
	return OpenAIAdmissionOptions{ContinuationEligible: true}
}

func (s *OpenAIGatewayService) OpenAIWSAccountWaitPlan(account *Account) *AccountWaitPlan {
	cfg := s.schedulingConfig()
	return &AccountWaitPlan{AccountID: account.ID, MaxConcurrency: account.Concurrency, Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting, Class: AccountWaitClassContinuation}
}

// OpenAIWSTurnSlotHold 返回轮间账号槽保留时长；0 表示关闭。
func (s *OpenAIGatewayService) OpenAIWSTurnSlotHold() time.Duration {
	if s == nil || s.cfg == nil || s.cfg.Gateway.OpenAIWS.TurnSlotHoldSeconds <= 0 {
		return 0
	}
	return time.Duration(s.cfg.Gateway.OpenAIWS.TurnSlotHoldSeconds) * time.Second
}
