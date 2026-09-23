export function supportsGroupCodexCLIOnly(platform: string): boolean {
  return platform === "openai";
}

export function normalizeGroupCodexCLIOnly(
  platform: string,
  enabled: boolean,
  allowAppServer: boolean,
): { codex_cli_only: boolean; codex_cli_only_allow_app_server: boolean } {
  const codexCLIOnly = supportsGroupCodexCLIOnly(platform) && enabled;
  return {
    codex_cli_only: codexCLIOnly,
    codex_cli_only_allow_app_server: codexCLIOnly && allowAppServer,
  };
}
