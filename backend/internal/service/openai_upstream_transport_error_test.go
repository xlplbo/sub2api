//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

type openAITransportTimeoutErr struct{}

func (openAITransportTimeoutErr) Error() string   { return "i/o timeout" }
func (openAITransportTimeoutErr) Timeout() bool   { return true }
func (openAITransportTimeoutErr) Temporary() bool { return true }

// TestClassifyUpstreamTransportError pins which transport-level upstream failures
// are "persistent" (retrying the same proxy/account is pointless — evict + alert)
// versus "transient" (a blip — fail over to a healthy account but do not evict).
//
// The motivating incident: a SOCKS5 proxy whose credentials expired returned
// `username/password authentication failed`, yet the account kept being scheduled.
func TestClassifyUpstreamTransportError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		persistent bool
	}{
		// Durable — config/credential/routing problems. Retrying same proxy won't help.
		{"socks5 proxy credential rejected", errors.New(`Post "https://chatgpt.com/backend-api/codex/responses": socks connect tcp 85.255.176.68:12324->chatgpt.com:443: username/password authentication failed`), true},
		{"proxy connection refused", errors.New(`proxyconnect tcp: dial tcp 1.2.3.4:1080: connect: connection refused`), true},
		{"no route to host", errors.New(`dial tcp 1.2.3.4:443: connect: no route to host`), true},
		{"dns resolution failure", errors.New(`dial tcp: lookup proxy.example.com: no such host`), true},
		{"network unreachable", errors.New(`dial tcp 1.2.3.4:443: connect: network is unreachable`), true},

		// Transient — a temporary blip. Fail over, but do NOT evict the account.
		{"client timeout", errors.New(`Post "https://chatgpt.com/...": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`), false},
		{"i/o timeout", errors.New(`dial tcp 1.2.3.4:443: i/o timeout`), false},
		{"connection reset by peer", errors.New(`read tcp 10.0.0.1:5->2.2.2.2:443: read: connection reset by peer`), false},
		{"unexpected eof", errors.New(`unexpected EOF`), false},
		{"broken pipe", errors.New(`write tcp 10.0.0.1:5->2.2.2.2:443: write: broken pipe`), false},

		{"nil error", nil, false},

		// ── Typed-error cases ──────────────────────────────────────────────
		// ECONNREFUSED wrapped in the canonical net.OpError shape Go produces.
		{
			"ECONNREFUSED via net.OpError",
			&net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
			},
			true,
		},
		// Bare syscall error (errors.Is traverses the chain).
		{"ECONNREFUSED bare", syscall.ECONNREFUSED, true},
		{"EHOSTUNREACH bare", syscall.EHOSTUNREACH, true},
		{"ENETUNREACH bare", syscall.ENETUNREACH, true},

		// *net.DNSError with IsNotFound — permanent DNS lookup failure.
		{
			"DNS not found (IsNotFound=true)",
			&net.DNSError{Err: "no such host", Name: "proxy.example.com", IsNotFound: true},
			true,
		},
		// *net.DNSError with IsNotFound=false — transient DNS timeout (not persistent).
		{
			"DNS timeout (IsNotFound=false)",
			&net.DNSError{Err: "i/o timeout", Name: "proxy.example.com", IsTimeout: true},
			false,
		},

		// context.Canceled — client gone; NOT classified as persistent.
		{"context.Canceled", context.Canceled, false},
		// context.DeadlineExceeded — slow upstream; NOT persistent.
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUpstreamTransportError(tc.err).Persistent
			if got != tc.persistent {
				t.Fatalf("classifyUpstreamTransportError(%q).Persistent = %v, want %v", errString(tc.err), got, tc.persistent)
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// TestClassifyOpenAITransportFailure pins the three-tier policy input: credential
// faults block immediately for 10 minutes, network faults (including dial-phase
// timeouts) count toward the short-block window, everything else only fails over.
func TestClassifyOpenAITransportFailure(t *testing.T) {
	dialTimeout := &net.OpError{Op: "dial", Net: "tcp", Err: openAITransportTimeoutErr{}}
	cases := []struct {
		name      string
		err       error
		dialPhase bool
		want      openAITransportFailureKind
	}{
		{"socks5 credential rejected", errors.New(`socks connect tcp 1.2.3.4:1080->chatgpt.com:443: username/password authentication failed`), false, openAITransportFailureCredential},
		{"http proxy 407", errors.New(`proxyconnect tcp: Proxy Authentication Required`), false, openAITransportFailureCredential},

		{"connection refused", errors.New(`dial tcp 127.0.0.1:10809: connect: connection refused`), false, openAITransportFailureNetwork},
		{"dns not found", &net.DNSError{Err: "no such host", Name: "proxy.example.com", IsNotFound: true}, false, openAITransportFailureNetwork},
		{"direct dial timeout", dialTimeout, false, openAITransportFailureNetwork},
		{"socks dial timeout", &net.OpError{Op: "socks connect", Net: "tcp", Err: dialTimeout}, false, openAITransportFailureNetwork},
		{"http proxy connect timeout", &net.OpError{Op: "proxyconnect", Net: "tcp", Err: dialTimeout}, false, openAITransportFailureNetwork},
		{"wrapped socks dial timeout", fmt.Errorf("Post %q: %w", "https://chatgpt.com/x", &net.OpError{Op: "socks connect", Net: "tcp", Err: dialTimeout}), false, openAITransportFailureNetwork},
		{"tls handshake timeout", errors.New(`Post "https://chatgpt.com/x": net/http: TLS handshake timeout`), false, openAITransportFailureNetwork},
		{"ws dial deadline exceeded", fmt.Errorf("failed to WebSocket dial: %w", context.DeadlineExceeded), true, openAITransportFailureNetwork},

		{"read timeout after connect", &net.OpError{Op: "read", Net: "tcp", Err: openAITransportTimeoutErr{}}, false, openAITransportFailureTransient},
		{"response header timeout", errors.New(`net/http: timeout awaiting response headers`), false, openAITransportFailureTransient},
		{"client timeout awaiting headers", errors.New(`context deadline exceeded (Client.Timeout exceeded while awaiting headers)`), false, openAITransportFailureTransient},
		{"deadline exceeded on http path", context.DeadlineExceeded, false, openAITransportFailureTransient},
		{"eof on ws dial", errors.New("EOF"), true, openAITransportFailureTransient},
		{"connection reset", errors.New(`read tcp 10.0.0.1:5->2.2.2.2:443: read: connection reset by peer`), false, openAITransportFailureTransient},
		{"nil", nil, false, openAITransportFailureTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyOpenAITransportFailure(tc.err, tc.dialPhase); got != tc.want {
				t.Fatalf("classifyOpenAITransportFailure(%v, dialPhase=%v) = %d, want %d", tc.err, tc.dialPhase, got, tc.want)
			}
		})
	}
}
