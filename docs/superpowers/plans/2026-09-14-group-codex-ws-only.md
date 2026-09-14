# 分组 Codex WS 准入实施计划

> 使用 superpowers:subagent-driven-development 分工实现和复核；用户已授权落地，不等待额外设计审批，不提交或部署。

**Goal:** 默认关闭的 `codex_ws_only` 贯穿分组配置与入站准入。

**Architecture:** 分组存储、管理 API、认证快照沿用已有布尔配置通路；独立 Gin 中间件在认证后、Composite 改写前拦截 Codex HTTP Responses 生成。前端创建与编辑表单统一提供开关。

**Tech Stack:** Go 1.27、Gin、Ent、PostgreSQL、Vue 3、TypeScript、Vitest。

**Spec:** [准入规格](../../../openspec/changes/group-codex-ws-only/specs/group-codex-ws-only/spec.md)

## 全局约束

- 默认关闭；仅 OpenAI/Composite；字段统一为 `codex_ws_only` / `CodexWSOnly`。
- 更新省略保留、false 关闭、复制保留、缓存刷新；不改上游协议和选号。
- 保持文件原有编码/BOM/换行；不格式化无关区域。
- Codex E2E 保留既有全局配置，不建立临时 CODEX_HOME。

## 任务 1：分组字段全链路

- [x] 先添加创建/更新/复制/认证快照及 DTO 契约测试，执行确认新增字段导致失败。
- [x] 修改 `backend/ent/schema/group.go`，新增 `239_group_codex_ws_only.sql`；贯穿 `service/group.go`、admin 输入、`admin_group.go`、duplicate、repository、API DTO 和认证缓存。接口为 `CodexWSOnly bool`，更新输入为 `*bool`。
- [x] 执行 `go generate ./ent`；运行新增测试及相关分组回归，检查生成物和缓存失效。

## 任务 2：前端开关

- [x] 在真实 GroupsView 组件测试覆盖默认关闭、编辑加载、true/false 提交、平台切换。
- [x] 修改 `frontend/src/types/index.ts`、`views/admin/GroupsView.vue`、中英文 `i18n/locales/*/admin/overview.ts`；开关显示于 OpenAI/Composite 分组，说明 HTTP 回退也会拒绝以及需重建会话。
- [x] 执行组件测试、typecheck、i18n completeness；提交 payload 使用 `codex_ws_only: boolean`。

## 任务 3：网关准入与集成验证

- [x] 先写 `group_codex_ws_only_test.go` 中间件和真实路由测试：关闭、HTTP/回退 HTTP、所有别名、Composite 原始组、WS、非 Codex、辅助接口、compact/v2、压缩体/读体失败。
- [x] 新建 `GroupCodexWSOnly()`，在 `gateway.go` 三条 Responses 路由链认证后挂载；拒绝返回 400 与固定错误 code，记录 ingress 拒绝原因。
- [x] 执行受影响后端包测试、必要 lint/build；尽可能以真实 Codex CLI 验证 400 仅请求一次，记录环境限制与源码版本证据。
- [x] 审阅完整 diff、文档与生成物，保存 `openspec/changes/group-codex-ws-only/verification.md`。
