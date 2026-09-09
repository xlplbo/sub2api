package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// openAITransportErrorTempUnschedDuration is how long an account is temporarily
// unscheduled after a credential-class transport failure (matches tokenRefreshTempUnschedDuration).
const openAITransportErrorTempUnschedDuration = 10 * time.Minute

// Network-class transport failures (dead proxy endpoint, DNS/routing, dial-phase
// timeout) can be a flap: the account is only unscheduled after
// openAITransportFailureThreshold of them within openAITransportFailureWindow,
// and for the (shorter, configurable) gateway.openai_transport_failure_block_seconds.
const (
	openAITransportFailureWindow       = 60 * time.Second
	openAITransportFailureThreshold    = 3
	openAITransportFailureBlockDefault = 120 * time.Second
)

// openAITransportFailoverBody is the OpenAI-format error body attached to the
// failover error for a transport-level failure. Kept identical to the legacy
// inline 502 body so the client-visible payload is unchanged if failover is
// ultimately exhausted.
var openAITransportFailoverBody = []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`)

// upstreamTransportErrorClass describes how to react to a transport-level upstream
// failure — i.e. the HTTP round-trip never completed (proxy / DNS / TCP / TLS
// error, no HTTP status code received).
type upstreamTransportErrorClass struct {
	// Persistent marks failures where retrying the same proxy/account is
	// pointless: expired or rejected proxy credentials, a dead proxy endpoint,
	// or DNS/routing failure. Such accounts should be temporarily unscheduled
	// (and alerted on) instead of being repeatedly scheduled into hard failures.
	Persistent bool
	// Credential narrows Persistent to rejected proxy credentials, which never
	// self-heal within minutes and therefore skip the short-block counting window.
	Credential bool
}

// credentialUpstreamTransportErrorMarkers / networkUpstreamTransportErrorMarkers are
// substrings (matched case-insensitively against the raw transport error) that
// indicate a durable proxy/network fault. Matched signals are intentionally specific
// failure *reasons*, not the operation (e.g. we match "connection refused", not
// "proxyconnect") so that a transient failure of the same operation (a proxy
// timeout) is NOT misclassified as durable.
var credentialUpstreamTransportErrorMarkers = []string{
	"authentication failed",         // SOCKS5 RFC1929 / proxy credentials rejected (expired account)
	"proxy authentication required", // HTTP proxy 407
}

var networkUpstreamTransportErrorMarkers = []string{
	"connection refused", // proxy/upstream endpoint down
	"no route to host",
	"network is unreachable",
	"no such host", // DNS resolution failure (bad/expired proxy hostname)
}

// classifyUpstreamTransportError decides whether a transport-level upstream error
// is durable (Persistent — evict the account + alert) or a transient blip
// (fail over to a healthy account but keep this one schedulable).
//
// Motivating incident: a SOCKS5 proxy whose subscription lapsed returned
// `username/password authentication failed`; the account was nonetheless
// rescheduled on every request, hard-failing users with 502s.
//
// Classification strategy (mirrors sanitizeStreamError in gateway_service.go):
//  1. Typed-error checks first (syscall constants, *net.DNSError) — portable and
//     unambiguous.
//  2. String-marker fallback for errors that have no typed form (e.g. the plain
//     string returned by golang.org/x/net/proxy for SOCKS5 credential rejection).
//     The network-layer string markers ("connection refused", "no route to host",
//     "network is unreachable", "no such host") are kept as a cross-platform safety
//     net even though the typed checks should cover them on modern Go+Linux.
func classifyUpstreamTransportError(err error) upstreamTransportErrorClass {
	if err == nil {
		return upstreamTransportErrorClass{}
	}

	// — Typed checks (preferred) ——————————————————————————————————————————————
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) {
		return upstreamTransportErrorClass{Persistent: true}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return upstreamTransportErrorClass{Persistent: true}
	}

	// — String-marker fallback ————————————————————————————————————————————————
	msg := strings.ToLower(err.Error())
	for _, marker := range credentialUpstreamTransportErrorMarkers {
		if strings.Contains(msg, marker) {
			return upstreamTransportErrorClass{Persistent: true, Credential: true}
		}
	}
	for _, marker := range networkUpstreamTransportErrorMarkers {
		if strings.Contains(msg, marker) {
			return upstreamTransportErrorClass{Persistent: true}
		}
	}
	return upstreamTransportErrorClass{}
}

// openAITransportFailureKind is the policy input derived from a transport error:
// how the OpenAI paths (HTTP and WS dial) react to it.
type openAITransportFailureKind int

const (
	// openAITransportFailureTransient: fail over, keep the account schedulable.
	openAITransportFailureTransient openAITransportFailureKind = iota
	// openAITransportFailureNetwork: fail over and count toward the short-block window.
	openAITransportFailureNetwork
	// openAITransportFailureCredential: fail over and unschedule for 10 minutes at once.
	openAITransportFailureCredential
)

// classifyOpenAITransportFailure maps a transport error to its policy kind.
// dialPhase is true when the whole failed operation was a dial (the WS handshake),
// so a context deadline there is a dial timeout; on the HTTP path only errors that
// carry a dial-phase *net.OpError (or the TLS handshake timeout) count, because a
// deadline while awaiting response headers is a slow upstream, not a dead proxy.
func classifyOpenAITransportFailure(err error, dialPhase bool) openAITransportFailureKind {
	if err == nil {
		return openAITransportFailureTransient
	}
	class := classifyUpstreamTransportError(err)
	switch {
	case class.Credential:
		return openAITransportFailureCredential
	case class.Persistent:
		return openAITransportFailureNetwork
	case dialPhase && errors.Is(err, context.DeadlineExceeded):
		return openAITransportFailureNetwork
	case isUpstreamDialTimeout(err):
		return openAITransportFailureNetwork
	}
	return openAITransportFailureTransient
}

func isUpstreamDialTimeout(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Timeout() {
		switch opErr.Op {
		case "dial", "socks connect", "proxyconnect":
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "tls handshake timeout")
}

// openAITransportFailureTracker counts network-class transport failures per
// account inside a sliding window; HTTP and WS dial failures share it because
// they hit the same proxy.
type openAITransportFailureTracker struct {
	mu       sync.Mutex
	failures map[int64][]time.Time
}

func (t *openAITransportFailureTracker) record(accountID int64, now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failures == nil {
		t.failures = make(map[int64][]time.Time)
	}
	kept := t.failures[accountID][:0]
	for _, at := range t.failures[accountID] {
		if now.Sub(at) < openAITransportFailureWindow {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	t.failures[accountID] = kept
	return len(kept)
}

func (t *openAITransportFailureTracker) reset(accountID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.failures, accountID)
}

func (s *OpenAIGatewayService) openAITransportFailureBlockDuration() time.Duration {
	if s == nil || s.cfg == nil {
		return openAITransportFailureBlockDefault
	}
	return time.Duration(s.cfg.Gateway.OpenAITransportFailureBlockSeconds) * time.Second
}

// applyOpenAITransportFailurePolicy applies the account-level block policy for a
// transport failure of the given kind and returns the failures counted in the
// current window and whether the account was unscheduled.
func (s *OpenAIGatewayService) applyOpenAITransportFailurePolicy(ctx context.Context, account *Account, safeErr string, kind openAITransportFailureKind) (int, bool) {
	if s == nil || account == nil {
		return 0, false
	}
	switch kind {
	case openAITransportFailureCredential:
		s.tempUnscheduleOpenAITransportError(ctx, account, safeErr, openAITransportErrorTempUnschedDuration)
		s.openaiTransportFailures.reset(account.ID)
		return 0, true
	case openAITransportFailureNetwork:
		failures := s.openaiTransportFailures.record(account.ID, time.Now())
		blockFor := s.openAITransportFailureBlockDuration()
		if blockFor <= 0 || failures < openAITransportFailureThreshold {
			return failures, false
		}
		s.tempUnscheduleOpenAITransportError(ctx, account, safeErr, blockFor)
		s.openaiTransportFailures.reset(account.ID)
		return failures, true
	}
	return 0, false
}

// handleOpenAIUpstreamTransportError handles a transport-level upstream failure
// (Do/DoWithTLS returned a non-HTTP error: proxy/DNS/TCP/TLS). It:
//  1. records the failure in Ops error logs (status 0, kind=request_error);
//  2. for credential faults (expired/rejected proxy creds) temporarily
//     unschedules the account (DB + in-memory) for 10 minutes; for network
//     faults (dead proxy, DNS/routing, dial-phase timeout) counts them per
//     account and unschedules for gateway.openai_transport_failure_block_seconds
//     on the 3rd within 60s — both log a stable warn event alert rules can key on;
//  3. returns an error that is *UpstreamFailoverError (so the handler fails over
//     to a healthy account) for all non-canceled errors, or a plain error for
//     context.Canceled (client gone — no failover, no eviction).
//
// It deliberately does NOT write to the response: the handler owns the response
// (failover, or a protocol-correct error once failover is exhausted).
//
// passthrough tags the Ops error event for the OpenAI passthrough forward path.
func (s *OpenAIGatewayService) handleOpenAIUpstreamTransportError(ctx context.Context, c *gin.Context, account *Account, err error, passthrough bool) error {
	safeErr := sanitizeUpstreamErrorMessage(err.Error())
	setOpsUpstreamError(c, 0, safeErr, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: 0,
		Passthrough:        passthrough,
		Kind:               "request_error",
		Message:            safeErr,
	})

	// Client disconnected: do NOT fail over to another account and do NOT evict
	// this one — the upstream never had a chance to exhibit a fault.
	if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return err
	}

	// Transport attempt reached the network path; count as Ollama Cloud activity.
	if s != nil {
		scheduleOllamaCloudUsageActivity(s.deferredService, account)
	}

	// 插件已把请求交给上游时，自动切换账号可能造成重复扣费或重复执行。
	var pluginErr *PluginTransportError
	if errors.As(err, &pluginErr) && pluginErr.RequestSent {
		return err
	}

	s.applyOpenAITransportFailurePolicy(ctx, account, safeErr, classifyOpenAITransportFailure(err, false))

	return &UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: openAITransportFailoverBody,
	}
}

// tempUnscheduleOpenAITransportError marks an account temporarily unschedulable
// for blockFor after a durable transport failure, both persistently (DB, survives
// restart) and in-memory (immediate scheduler effect before the DB/account cache propagates).
//
// Log semantics:
//   - "openai.account_temp_unscheduled_transport" — emitted ONLY after a
//     successful DB write (both in-memory + persisted).
//   - "openai.account_temp_unscheduled_transport_memory_only" — emitted when
//     accountRepo is nil (in-memory only; no persistence).
//   - "openai.account_temp_unscheduled_transport_failed" — DB write attempted
//     but returned an error.
func (s *OpenAIGatewayService) tempUnscheduleOpenAITransportError(ctx context.Context, account *Account, safeErr string, blockFor time.Duration) {
	if s == nil || account == nil {
		return
	}
	until := time.Now().Add(blockFor)
	reason := "upstream transport error (proxy/network): " + safeErr

	// Immediate in-memory block so this process skips the account until the
	// persisted cooldown is visible on the scheduling Account. Selection is
	// fail-open: empty snapshot/DB cooldown fields drop a stale local block.
	s.BlockAccountScheduling(account, until, "transport_error")

	if s.accountRepo == nil {
		// No DB configured — block is in-memory only; emit a distinct event so
		// operators are not misled into thinking the block survived a restart.
		logger.L().With(zap.String("component", "service.openai_gateway")).Warn(
			"openai.account_temp_unscheduled_transport_memory_only",
			zap.Int64("account_id", account.ID),
			zap.String("account_name", account.Name),
			zap.String("platform", account.Platform),
			zap.Time("until", until),
			zap.String("reason", reason),
		)
		return
	}

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAccountStateUpdateTimeout)
	defer cancel()
	if err := s.accountRepo.SetTempUnschedulable(bgCtx, account.ID, until, reason); err != nil {
		logger.L().With(zap.String("component", "service.openai_gateway")).Warn(
			"openai.account_temp_unscheduled_transport_failed",
			zap.Int64("account_id", account.ID),
			zap.Error(err),
		)
		return
	}

	// DB write succeeded: both in-memory and persisted.
	logger.L().With(zap.String("component", "service.openai_gateway")).Warn(
		"openai.account_temp_unscheduled_transport",
		zap.Int64("account_id", account.ID),
		zap.String("account_name", account.Name),
		zap.String("platform", account.Platform),
		zap.Time("until", until),
		zap.String("reason", reason),
	)
}
