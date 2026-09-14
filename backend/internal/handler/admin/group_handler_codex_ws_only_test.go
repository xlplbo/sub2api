package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupRequestsDecodeCodexWSOnly(t *testing.T) {
	var createReq CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"ws-only","codex_ws_only":true}`), &createReq))
	require.True(t, createReq.CodexWSOnly)

	var updateReq UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"codex_ws_only":false}`), &updateReq))
	require.NotNil(t, updateReq.CodexWSOnly)
	require.False(t, *updateReq.CodexWSOnly)

	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.CodexWSOnly)
}
