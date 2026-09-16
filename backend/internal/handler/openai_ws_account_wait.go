package handler

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"go.uber.org/zap"
)

const (
	openAIWSAccountWaitPhaseInitial    = "initial"
	openAIWSAccountWaitPhaseSubsequent = "subsequent"
	openAIWSAccountWaitPhaseRetry      = "retry"
)

type openAIWSAccountWaitBudget struct {
	deadline     time.Time
	turn         int
	requestSent  atomic.Bool
	continuation bool
}

func (b *openAIWSAccountWaitBudget) nextTurn() {
	b.turn++
	b.deadline = time.Time{}
	b.continuation = true
}

// 透传后续轮的帧在准入前一直由网关持有、尚未写入上游，可与其他模式一样排队等槽。
func (b *openAIWSAccountWaitBudget) canWait(mode, phase string) bool {
	if mode == service.OpenAIWSIngressModeOff {
		return false
	}
	if mode != service.OpenAIWSIngressModePassthrough || phase == openAIWSAccountWaitPhaseSubsequent {
		return true
	}
	return !b.continuation && !b.requestSent.Load()
}

func (b *openAIWSAccountWaitBudget) waitDeadline(timeout time.Duration, retryDeadline time.Time) time.Time {
	deadline := time.Now().Add(timeout)
	if b.deadline.IsZero() || deadline.Before(b.deadline) {
		b.deadline = deadline
	}
	if !retryDeadline.IsZero() && retryDeadline.Before(b.deadline) {
		return retryDeadline
	}
	return b.deadline
}

func (b *openAIWSAccountWaitBudget) expired() bool {
	return !b.deadline.IsZero() && !time.Now().Before(b.deadline)
}

func openAIWSAccountBusyError() error {
	return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "account is busy, please retry later", nil)
}

func closeWSAccountAdmission(ctx context.Context, conn *coderws.Conn, err error) {
	if errors.Is(context.Cause(ctx), service.ErrOpenAIWSIngressLeaseLost) {
		closeOpenAIClientWS(conn, coderws.StatusTryAgainLater, "websocket ingress capacity lease lost; please reconnect")
		return
	}
	var closeErr *service.OpenAIWSClientCloseError
	if errors.As(err, &closeErr) {
		closeOpenAIClientWS(conn, closeErr.StatusCode(), closeErr.Reason())
		return
	}
	closeOpenAIClientWS(conn, coderws.StatusGoingAway, "websocket request canceled")
}

// enterWSAccountWaitQueue 按等待计划的类别选计数键：续聊类进 wait:account:cont，其余进 wait:account。
func (h *OpenAIGatewayHandler) enterWSAccountWaitQueue(ctx context.Context, accountID int64, plan *service.AccountWaitPlan) (bool, func(), error) {
	if plan.Class == service.AccountWaitClassContinuation {
		canWait, err := h.concurrencyHelper.IncrementAccountContinuationWaitCount(ctx, accountID, plan.MaxWaiting)
		return canWait, func() { h.concurrencyHelper.DecrementAccountContinuationWaitCount(ctx, accountID) }, err
	}
	canWait, err := h.concurrencyHelper.IncrementAccountWaitCount(ctx, accountID, plan.MaxWaiting)
	return canWait, func() { h.concurrencyHelper.DecrementAccountWaitCount(ctx, accountID) }, err
}

func (h *OpenAIGatewayHandler) acquireWSAccountSlot(ctx context.Context, account *service.Account, maxConcurrency int, plan *service.AccountWaitPlan, budget *openAIWSAccountWaitBudget, mode, phase string, retryDeadline time.Time, log *zap.Logger) (func(), error) {
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	if budget.expired() {
		return nil, openAIWSAccountBusyError()
	}
	planClass := service.AccountWaitClassLegacy
	if plan != nil {
		planClass = plan.Class
	}
	fast, err := h.concurrencyHelper.TryAcquireAccountSlotForPlan(ctx, account.ID, maxConcurrency, planClass)
	if err != nil {
		return nil, service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire account concurrency slot", err)
	}
	if fast != nil && fast.Acquired {
		if ctx.Err() != nil {
			fast.ReleaseFunc()
			return nil, context.Cause(ctx)
		}
		return fast.ReleaseFunc, nil
	}
	if !account.IsOpenAI() || !budget.canWait(mode, phase) || plan == nil || plan.Timeout <= 0 || plan.MaxWaiting <= 0 {
		return nil, openAIWSAccountBusyError()
	}
	deadline := budget.waitDeadline(plan.Timeout, retryDeadline)
	if !time.Now().Before(deadline) {
		return nil, openAIWSAccountBusyError()
	}
	canWait, leaveQueue, err := h.enterWSAccountWaitQueue(ctx, account.ID, plan)
	if err != nil {
		return nil, service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to enter account wait queue", err)
	}
	if !canWait {
		log.Info("openai.websocket_account_wait_finished", zap.Int64("account_id", account.ID), zap.String("mode", mode), zap.String("phase", phase), zap.String("class", plan.Class.String()), zap.String("reason", "queue_full"))
		return nil, openAIWSAccountBusyError()
	}
	defer leaveQueue()
	observedCtx, endObservation := service.BeginOpenAIWSAccountWait(ctx)
	defer endObservation()
	waitCtx, cancel := context.WithDeadline(observedCtx, deadline)
	defer cancel()
	started := time.Now()
	log.Info("openai.websocket_account_wait_started", zap.Int64("account_id", account.ID), zap.String("mode", mode), zap.String("phase", phase), zap.Int("turn", budget.turn), zap.Int("max_concurrency", maxConcurrency), zap.Int("max_waiting", plan.MaxWaiting), zap.String("class", plan.Class.String()), zap.Int64("budget_ms", time.Until(deadline).Milliseconds()))
	release, err := waitForConcurrencySlot(waitCtx, func() (*service.AcquireResult, error) {
		if plan.Class == service.AccountWaitClassNewSession && h.concurrencyHelper.HasContinuationWaiters(waitCtx, account.ID) {
			return &service.AcquireResult{}, nil
		}
		return h.concurrencyHelper.concurrencyService.AcquireAccountSlot(waitCtx, account.ID, maxConcurrency)
	}, nil, nil)
	if err == nil && waitCtx.Err() != nil {
		if release != nil {
			release()
		}
		err = context.Cause(waitCtx)
	}
	reason := "acquired"
	if err != nil {
		switch {
		case ctx.Err() != nil:
			reason = "canceled"
		case observedCtx.Err() != nil:
			reason = "client_closed"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "wait_timeout"
		default:
			reason = "acquire_error"
		}
	}
	log.Info("openai.websocket_account_wait_finished", zap.Int64("account_id", account.ID), zap.String("mode", mode), zap.String("phase", phase), zap.Int("turn", budget.turn), zap.Int64("wait_ms", time.Since(started).Milliseconds()), zap.String("class", plan.Class.String()), zap.String("reason", reason))
	if err == nil {
		return release, nil
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	if observedCtx.Err() != nil {
		cause := context.Cause(observedCtx)
		var closeErr *service.OpenAIWSClientCloseError
		if errors.As(cause, &closeErr) {
			return nil, closeErr
		}
		return nil, service.NewOpenAIWSClientCloseError(coderws.StatusGoingAway, "websocket client disconnected while waiting", cause)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, openAIWSAccountBusyError()
	}
	return nil, service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire account concurrency slot", err)
}
