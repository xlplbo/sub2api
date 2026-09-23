package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupCodexCLIOnlyMigration(t *testing.T) {
	content, err := FS.ReadFile("241_group_codex_cli_only.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS codex_cli_only BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS codex_cli_only_allow_app_server BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.codex_cli_only IS")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.codex_cli_only_allow_app_server IS")
}
