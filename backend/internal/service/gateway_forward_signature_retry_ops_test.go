//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// signatureRetryUpstreamStub replays one response or error per call.
type signatureRetryUpstreamStub struct {
	steps []func() (*http.Response, error)
	calls int
}

func (u *signatureRetryUpstreamStub) next() (*http.Response, error) {
	if u.calls >= len(u.steps) {
		return nil, errors.New("unexpected upstream call")
	}
	step := u.steps[u.calls]
	u.calls++
	return step()
}

func (u *signatureRetryUpstreamStub) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return u.next()
}

func (u *signatureRetryUpstreamStub) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	return u.next()
}

func anthropicSignatureErrorResponse(message string) func() (*http.Response, error) {
	return func() (*http.Response, error) {
		body := fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`, message)
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
}

func runToolDowngradeSignatureRetry(t *testing.T, toolRetryErr error) *OpsUpstreamErrorEvent {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"model":"claude-sonnet-4-5","stream":false,"max_tokens":16,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"bad"},{"type":"tool_use","id":"tu_1","name":"f","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok"}]}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	upstream := &signatureRetryUpstreamStub{steps: []func() (*http.Response, error){
		anthropicSignatureErrorResponse("messages.1.content.0: Invalid `signature` in `thinking` block"),
		anthropicSignatureErrorResponse("messages.1.content.1: tool_use block has an invalid signature"),
		func() (*http.Response, error) { return nil, toolRetryErr },
	}}
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
		settingService:       &SettingService{settingRepo: &settingRepoStub{values: map[string]string{}}, cfg: cfg},
	}

	_, _ = svc.Forward(context.Background(), c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)
	require.Equal(t, 3, upstream.calls, "signature retry must reach the tool-downgrade attempt")

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	for _, ev := range events {
		if ev.Kind == "signature_retry_tools_request_error" {
			return ev
		}
	}
	t.Fatalf("no signature_retry_tools_request_error event in %+v", events)
	return nil
}

func TestGatewayForward_ToolDowngradeSignatureRetryCanceledIsMarked(t *testing.T) {
	ev := runToolDowngradeSignatureRetry(t, fmt.Errorf("Post \"https://api.anthropic.com/v1/messages\": %w", context.Canceled))
	require.Equal(t, opsUpstreamReasonRequestCanceled, ev.Reason, "a client cancel is not a proxy failure")
}

func TestGatewayForward_ToolDowngradeSignatureRetryTransportFailureStaysCounted(t *testing.T) {
	ev := runToolDowngradeSignatureRetry(t, errors.New("socks connect tcp 127.0.0.1:10838: general SOCKS server failure"))
	require.Empty(t, ev.Reason)
}
