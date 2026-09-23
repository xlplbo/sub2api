package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newGroupCodexCLIOnlyTestContext(header map[string]string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	for k, v := range header {
		c.Request.Header.Set(k, v)
	}
	return c
}

func TestGroup_CodexCLIOnlyEnabled(t *testing.T) {
	var nilGroup *Group
	require.False(t, nilGroup.CodexCLIOnlyEnabled())
	require.True(t, (&Group{Platform: PlatformOpenAI, CodexCLIOnly: true}).CodexCLIOnlyEnabled())
	require.False(t, (&Group{Platform: PlatformOpenAI}).CodexCLIOnlyEnabled())
	require.False(t, (&Group{Platform: PlatformComposite, CodexCLIOnly: true}).CodexCLIOnlyEnabled(), "只对 openai 平台生效")
	require.False(t, (&Group{Platform: PlatformAnthropic, CodexCLIOnly: true}).CodexCLIOnlyEnabled(), "只对 openai 平台生效")
}

func TestDetectGroupCodexClientRestriction(t *testing.T) {
	policy := CodexRestrictionPolicy{EngineFingerprintSignals: openai.DefaultEngineFingerprintSignals}
	official := map[string]string{
		"User-Agent":        "codex_cli_rs/0.153.4 (Windows 11; x86_64) WindowsTerminal",
		"originator":        "codex_cli_rs",
		"x-codex-window-id": "window-1",
	}
	appServer := map[string]string{
		"User-Agent":        "claude_probe/0.153.4 (Windows 10.0.26200; x86_64) unknown (claude_probe; 0.1)",
		"originator":        "claude_probe",
		"x-codex-window-id": "window-2",
	}
	script := map[string]string{"User-Agent": "ctrl-probe/1", "session_id": "sess-1"}
	group := &Group{Platform: PlatformOpenAI, CodexCLIOnly: true}

	t.Run("分组未开：不限制", func(t *testing.T) {
		result := DetectGroupCodexClientRestriction(newGroupCodexCLIOnlyTestContext(script), &Group{Platform: PlatformOpenAI}, policy, false, nil)
		require.False(t, result.Enabled)
	})

	t.Run("非官方客户端：拒绝", func(t *testing.T) {
		result := DetectGroupCodexClientRestriction(newGroupCodexCLIOnlyTestContext(script), group, policy, false, nil)
		require.True(t, result.Enabled)
		require.False(t, result.Matched)
		require.Equal(t, CodexClientRestrictionReasonNotMatchedUA, result.Reason)
	})

	t.Run("官方客户端：放行", func(t *testing.T) {
		result := DetectGroupCodexClientRestriction(newGroupCodexCLIOnlyTestContext(official), group, policy, false, nil)
		require.True(t, result.Enabled)
		require.True(t, result.Matched)
		require.Equal(t, CodexClientRestrictionReasonMatchedUA, result.Reason)
	})

	t.Run("官方 UA 缺引擎指纹：拒绝", func(t *testing.T) {
		header := map[string]string{"User-Agent": official["User-Agent"], "originator": official["originator"]}
		result := DetectGroupCodexClientRestriction(newGroupCodexCLIOnlyTestContext(header), group, policy, false, nil)
		require.False(t, result.Matched)
		require.Equal(t, CodexClientRestrictionReasonMissingEngineFingerprint, result.Reason)
	})

	t.Run("app-server 客户端：仅分组放行开关打开时通过", func(t *testing.T) {
		denied := DetectGroupCodexClientRestriction(newGroupCodexCLIOnlyTestContext(appServer), group, policy, false, nil)
		require.False(t, denied.Matched)

		allowGroup := &Group{Platform: PlatformOpenAI, CodexCLIOnly: true, CodexCLIOnlyAllowAppServer: true}
		allowed := DetectGroupCodexClientRestriction(newGroupCodexCLIOnlyTestContext(appServer), allowGroup, policy, false, nil)
		require.True(t, allowed.Matched)
		require.Equal(t, CodexClientRestrictionReasonMatchedAppServerClient, allowed.Reason)
	})

	t.Run("gateway.force_codex_cli：旁路放行", func(t *testing.T) {
		result := DetectGroupCodexClientRestriction(newGroupCodexCLIOnlyTestContext(script), group, policy, true, nil)
		require.True(t, result.Matched)
		require.Equal(t, CodexClientRestrictionReasonForceCodexCLI, result.Reason)
	})
}

func TestCodexGroupClientRestrictionMessage(t *testing.T) {
	require.Equal(t, "This group only allows Codex official clients",
		CodexGroupClientRestrictionMessage(CodexClientRestrictionDetectionResult{Enabled: true, Reason: CodexClientRestrictionReasonNotMatchedUA}))
	versionResult := CodexClientRestrictionDetectionResult{
		Enabled: true, Reason: CodexClientRestrictionReasonVersionTooLow, DetectedVersion: "0.39.0", MinCodexVersion: "0.42.0",
	}
	require.Equal(t, CodexClientRestrictionMessage(versionResult), CodexGroupClientRestrictionMessage(versionResult))
}

func TestCodexRestrictionPolicyOrDefault_NilSettingServiceKeepsFingerprintGate(t *testing.T) {
	policy := CodexRestrictionPolicyOrDefault(context.Background(), nil)
	require.Equal(t, openai.DefaultEngineFingerprintSignals, policy.EngineFingerprintSignals)
}

func TestLogGroupCodexCLIOnlyRejection_OnlyLogsRejected(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()
	c := newGroupCodexCLIOnlyTestContext(map[string]string{"User-Agent": "ctrl-probe/1"})

	LogGroupCodexCLIOnlyRejection(context.Background(), c, 7, 21, CodexClientRestrictionDetectionResult{
		Enabled: true, Matched: true, Reason: CodexClientRestrictionReasonMatchedUA,
	}, nil)
	require.False(t, logSink.ContainsMessage("分组 codex_cli_only"))

	LogGroupCodexCLIOnlyRejection(context.Background(), c, 7, 21, CodexClientRestrictionDetectionResult{
		Enabled: true, Matched: false, Reason: CodexClientRestrictionReasonNotMatchedUA,
	}, nil)
	require.True(t, logSink.ContainsMessage("OpenAI 分组 codex_cli_only 拒绝非官方客户端请求"))
	require.True(t, logSink.ContainsFieldValue("request_user_agent", "ctrl-probe/1"))
	require.True(t, logSink.ContainsFieldValue("reject_reason", CodexClientRestrictionReasonNotMatchedUA))
}
