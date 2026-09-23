package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupRequestsDecodeCodexCLIOnly(t *testing.T) {
	var createReq CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"codex","codex_cli_only":true,"codex_cli_only_allow_app_server":true}`), &createReq))
	require.True(t, createReq.CodexCLIOnly)
	require.True(t, createReq.CodexCLIOnlyAllowAppServer)

	var updateReq UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"codex_cli_only":false,"codex_cli_only_allow_app_server":false}`), &updateReq))
	require.NotNil(t, updateReq.CodexCLIOnly)
	require.False(t, *updateReq.CodexCLIOnly)
	require.NotNil(t, updateReq.CodexCLIOnlyAllowAppServer)
	require.False(t, *updateReq.CodexCLIOnlyAllowAppServer)

	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.CodexCLIOnly)
	require.Nil(t, omitted.CodexCLIOnlyAllowAppServer)
}
