# Codex 仅允许 WS 接入

在管理端创建或编辑 OpenAI / Composite 分组时，可开启“Codex 仅允许 WS 接入”。管理 API 字段为 `codex_ws_only`，默认 `false`，现有分组迁移后也保持关闭。

开启后，识别为 Codex 的 HTTP Responses 生成请求会在账号调度之前返回：

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "codex_websocket_required",
    "message": "This group requires Codex to connect via WebSocket. Enable WebSocket and start a new client session."
  }
}
```

HTTP 状态码为 **400**。直接 HTTP 和 Codex 从 WS 回退后的 HTTP 同样拒绝。已核对的 Codex CLI 0.153.4 将此响应视为不可重试的 InvalidRequest；用户仍可手动再次请求，第三方客户端的重试行为由其自身决定。该策略不改变 WS 连接此前已经发生的重连。

启用客户端 WS 后，请重新建立客户端运行会话。Codex 已发生 HTTP 回退的运行会话可能继续使用 HTTP，单纯重发消息仍会被拒绝。

准入覆盖 `/v1/responses`、`/responses`、`/backend-api/codex/responses` 的 POST 生成入口，包含原生流式 compaction v2。合法 WS 握手与消息、非 Codex 客户端、模型列表、独立搜索、`/responses/compact`、`/responses/input_tokens` 和实际由 OpenAI 兼容 handler 提升为旧式 compact 的非流式 body-signal 请求不受此开关限制。Composite 若把带压缩标记的请求路由到 Anthropic 等普通生成 handler，仍在选号前拒绝。

Composite 按 API Key 原始绑定分组检查。分组复制保留配置；管理 API 更新时省略字段保留原值，传 `false` 关闭；切换到其他平台时清零。配置更新沿用分组认证缓存失效机制。关闭开关即可恢复原准入行为，无需回滚数据库。

认证快照版本升为 v25，新实例读取不含此字段的 v24 或更旧快照时会回源读取。迁移 239 同时补齐数据库触发器：开关实际变化时，在同一事务中写入所属 API Key 的失效 outbox，由现有消费者处理，兜底直接 SQL 更新或应用层失效失败；重复设置相同值不入队。混合部署中的旧实例仍不支持此准入规则，启用前应完成所有实例升级。

此开关只限制客户端入站，账号选择与上游 `http_bridge` / HTTP 回退配置沿用现有规则。Codex 身份根据原始请求的官方 UA 家族或 originator 识别；请求头可被改写，因此它是兼容准入策略，不能作为防伪身份认证。

验证：

```sh
cd backend
go test -p 1 -tags=unit ./internal/server/middleware ./internal/server/routes -run 'TestGroupCodexWSOnly|TestGatewayRoutesCodexWSOnly' -count=1
```

使用已安装的 Codex CLI 验证真实的 HTTP 错误与原生 WS 回退后不重试：设置 `CODEX_WS_ONLY_E2E_BIN` 为原生可执行文件路径，`CODEX_WS_ONLY_E2E_PROVIDER` 为已有全局配置中的 provider 名，再执行：

```sh
go test -p 1 -tags=e2e ./internal/server/routes -run TestGatewayCodexWSOnlyCLIStopsAfterHTTP400 -count=1 -v
```

该测试启动本地网关准入路由，HTTP 400 来自真实中间件，WS 握手故障由本地测试端模拟。测试仅为此次运行覆盖 provider URL 和 WS 开关，保留现有全局模型、认证、MCP、插件、权限和 Windows sandbox；不创建临时 `CODEX_HOME`，不访问上游账号，也不替代部署后的完整链路验收。
