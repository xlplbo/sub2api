import { describe, expect, it } from "vitest";

import {
  normalizeGroupCodexCLIOnly,
  supportsGroupCodexCLIOnly,
} from "../groupsCodexCLIOnly";
import enAccounts from "@/i18n/locales/en/admin/accounts";
import en from "@/i18n/locales/en/admin/overview";
import zhAccounts from "@/i18n/locales/zh/admin/accounts";
import zh from "@/i18n/locales/zh/admin/overview";

describe("groupsCodexCLIOnly", () => {
  it("only supports OpenAI groups", () => {
    expect(supportsGroupCodexCLIOnly("openai")).toBe(true);
    expect(supportsGroupCodexCLIOnly("composite")).toBe(false);
    expect(supportsGroupCodexCLIOnly("anthropic")).toBe(false);
  });

  it("clears stale state on unsupported platforms and when the main switch is off", () => {
    expect(normalizeGroupCodexCLIOnly("openai", true, true)).toEqual({
      codex_cli_only: true,
      codex_cli_only_allow_app_server: true,
    });
    expect(normalizeGroupCodexCLIOnly("openai", false, true)).toEqual({
      codex_cli_only: false,
      codex_cli_only_allow_app_server: false,
    });
    expect(normalizeGroupCodexCLIOnly("anthropic", true, true)).toEqual({
      codex_cli_only: false,
      codex_cli_only_allow_app_server: false,
    });
  });

  it("provides localized switch labels", () => {
    for (const locale of [zh, en]) {
      expect(locale.groups.codexCliOnly).toMatchObject({
        title: expect.any(String),
        hint: expect.stringContaining("WebSocket"),
        allowAppServer: expect.stringContaining("app-server"),
        allowAppServerHint: expect.any(String),
      });
    }
  });

  it("uses the same switch labels as the account-level setting", () => {
    for (const [group, account] of [
      [zh, zhAccounts],
      [en, enAccounts],
    ] as const) {
      expect(group.groups.codexCliOnly.title).toBe(account.accounts.openai.codexCLIOnly);
      expect(group.groups.codexCliOnly.allowAppServer).toBe(
        account.accounts.openai.codexCLIOnlyAppServer,
      );
    }
  });
});
