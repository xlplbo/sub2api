package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupMapperExposesCodexCLIOnlyOnlyToAdmins(t *testing.T) {
	group := &service.Group{
		ID: 8, Name: "codex", Platform: service.PlatformOpenAI, Status: service.StatusActive,
		CodexCLIOnly: true, CodexCLIOnlyAllowAppServer: true,
	}

	userJSON, err := json.Marshal(GroupFromService(group))
	require.NoError(t, err)
	require.NotContains(t, string(userJSON), "codex_cli_only")

	adminJSON, err := json.Marshal(GroupFromServiceAdmin(group))
	require.NoError(t, err)
	require.Contains(t, string(adminJSON), `"codex_cli_only":true`)
	require.Contains(t, string(adminJSON), `"codex_cli_only_allow_app_server":true`)
}
