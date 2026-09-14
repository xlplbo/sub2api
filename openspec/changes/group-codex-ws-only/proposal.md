# 分组 Codex WS 准入

增加默认关闭的分组配置 `codex_ws_only`，用于统一 Codex 客户端的入站传输方式。启用后，HTTP Responses 生成请求在账号调度前返回不可重试的 HTTP 400；不区分首次 HTTP 与原生 WS 回退后的 HTTP。

适用于 OpenAI 和 Composite 分组。Composite 使用 API Key 原始绑定分组的配置。账号调度、上游 WebSocket、`http_bridge` 和 HTTP 回退沿用现有行为。

详细契约见 [spec.md](specs/group-codex-ws-only/spec.md)，实现与验证见 [执行计划](../../../docs/superpowers/plans/2026-09-14-group-codex-ws-only.md)。
