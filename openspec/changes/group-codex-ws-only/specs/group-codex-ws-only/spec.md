## ADDED Requirements

### Requirement: 分组配置与默认兼容

系统 SHALL 提供管理端布尔配置 `codex_ws_only`，适用于 OpenAI/Composite 分组，数据库与新建界面默认关闭。更新省略该字段 SHALL 保留原值，显式 false SHALL 关闭；切到不适用的平台 SHALL 清零。分组复制 SHALL 保留该值。认证缓存 SHALL 保存并恢复该字段，管理端更新 SHALL 使所属 API Key 认证缓存失效。

#### Scenario: 默认关闭
- **WHEN** 旧分组经过迁移，或新分组没有设置该字段
- **THEN** HTTP 与 WS 请求沿用原有行为

#### Scenario: 旧认证快照回源
- **WHEN** 新实例读取不含 `codex_ws_only` 的 v24 或更旧认证快照
- **THEN** 系统 SHALL 拒绝使用旧快照并回源读取配置，不得将缺失字段当作关闭开关

#### Scenario: 分组配置持久化失效
- **WHEN** 数据库中的 `codex_ws_only` 从 false 改为 true 或从 true 改为 false
- **THEN** 同一事务 SHALL 为该分组未删除且密钥非空的 API Key 写入认证缓存失效 outbox，作为直接 SQL 更新或应用层失效失败的兜底
- **AND** 仅重复设置相同值 SHALL 不产生失效事件，原有分组字段触发器语义 SHALL 保留

### Requirement: Codex HTTP 生成准入

系统 SHALL 在认证之后、Composite 路由改写与账号调度之前，根据 API Key 原始分组执行准入。客户端识别 SHALL 复用官方 Codex UA 家族严格识别或官方 originator 识别；识别使用原始请求头，不使用出站改写身份或全局 ForceCodexCLI。该识别是兼容规则，不是不可伪造的身份认证。

#### Scenario: 拒绝直接或回退 HTTP
- **WHEN** 开关开启且识别为 Codex 的 POST 请求命中 `/v1/responses`、`/responses` 或 `/backend-api/codex/responses` 生成入口（包括流式 compaction v2）
- **THEN** 系统 SHALL 返回 HTTP 400 和 `error.type=invalid_request_error`、`error.code=codex_websocket_required`，说明启用 WS 并重建客户端会话；后续路由与账号调度 SHALL 不执行
- **AND** 拒绝 SHALL 记录为准入拒绝，不记为上游故障

#### Scenario: 保留辅助接口和其他客户端
- **WHEN** 请求为非 Codex、WS GET 握手、模型发现、独立搜索、`/responses/compact`、`/responses/input_tokens`，或按现有规则提升为旧式 compact 的非流式 body-signal 请求
- **THEN** 此开关 SHALL 不拦截，请求体 SHALL 可由下游完整读取，现有验证和转发继续生效

#### Scenario: 不限制上游
- **WHEN** Codex 通过 WS 入站
- **THEN** 原有账号协议选择、`http_bridge`、上游 HTTP 回退 SHALL 保持不变

#### Scenario: Composite 旧式压缩豁免
- **WHEN** 已启用限制的 Codex 非流式请求通过 body-signal 申请旧式 compact
- **THEN** 前置检查 SHALL 保留原始分组的准入决定，并在 Responses 路由分发时确认目标 handler 支持旧式 compact 提升
- **AND** 若目标 handler 会将其作为普通生成（例如 Composite 路由到 Anthropic），SHALL 在进入该 handler 和账号调度前返回相同 400 策略错误；普通生成的拒绝仍发生在 Composite 路由改写前

### Requirement: 客户端重试边界

HTTP 400 用于让已核对的 Codex CLI 0.153.4 将错误识别为 InvalidRequest 并停止自动重试该请求。此配置 SHALL 不声称能阻止所有第三方客户端重试、用户手动重试或改变已经发生的 WS 重连。Codex 回退后当前客户端运行会话可能继续使用 HTTP，操作说明 SHALL 提示重新建立客户端会话。
