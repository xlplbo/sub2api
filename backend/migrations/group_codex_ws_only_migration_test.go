package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupCodexWSOnlyMigration(t *testing.T) {
	content, err := FS.ReadFile("239_group_codex_ws_only.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		"ADD COLUMN IF NOT EXISTS codex_ws_only BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.codex_ws_only")
}
