ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS codex_cli_only BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS codex_cli_only_allow_app_server BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.codex_cli_only IS
    'Whether every gateway request of this OpenAI group must come from a Codex official client';

COMMENT ON COLUMN groups.codex_cli_only_allow_app_server IS
    'Whether this codex_cli_only group also admits Codex app-server clients, still subject to the engine fingerprint gate';
