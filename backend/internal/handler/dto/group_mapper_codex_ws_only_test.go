package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupMapperExposesCodexWSOnlyOnlyToAdmins(t *testing.T) {
	group := &service.Group{
		ID: 7, Name: "ws-only", Platform: service.PlatformOpenAI, Status: service.StatusActive, CodexWSOnly: true,
	}

	userJSON, err := json.Marshal(GroupFromService(group))
	require.NoError(t, err)
	require.NotContains(t, string(userJSON), "codex_ws_only")

	adminJSON, err := json.Marshal(GroupFromServiceAdmin(group))
	require.NoError(t, err)
	require.Contains(t, string(adminJSON), `"codex_ws_only":true`)
}
