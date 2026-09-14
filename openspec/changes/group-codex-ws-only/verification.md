# 分组 Codex WS 准入验证

验证日期：2026-09-14。实现基于 `cb2a19a744059cb07f81c4411d0bdeed905fd4fd`，分支 `codex/group-codex-ws-only`。

## 行为与覆盖

- 分组 `codex_ws_only` 默认关闭，OpenAI/Composite 创建与更新、显式 false、更新省略、平台切换归零、复制、管理员 DTO、认证投影及缓存快照已接通。
- 认证快照升为 v25；旧 v24 JSON 缺少此字段时，新实例通过 `GetByKey` 回源一次并恢复数据库中的开启状态。迁移 239 补齐 `codex_ws_only` 的事务内 outbox 失效，沿用原触发器的其他字段与密钥筛选规则。
- 三条实际 Responses 路由链在认证之后、普通生成路由改写及选号之前执行准入；HTTP 首次请求和回退请求均返回 `400 / invalid_request_error / codex_websocket_required`。
- 真实 WS 握手后消息往返通过；非 Codex、模型发现、搜索、compact/input_tokens 等辅助路径不受此策略限制。
- 非流式 body-signal 仅在实际支持旧 compact 的 handler 上豁免。Composite→Anthropic 的同形态请求在 handler/选号之前拒绝；OpenAI 和 Composite→OpenAI 继续进入旧 compact 处理器。
- gzip、预读取体、请求体完整回填、读体失败、超限、原生流式 compaction v2、路径别名和伪造 POST Upgrade 均有测试。
- 未修改账号调度、上游 WS、`http_bridge` 或 HTTP 回退逻辑。

## 已执行检查

| 检查 | 结果 |
| --- | --- |
| `go generate ./ent` | 通过；生成物已保存，依赖文件无差异 |
| `go test -p 1 -tags=unit ./internal/server/middleware ./internal/server/routes -count=1` | 最终代码通过 |
| `go test -p 1 -tags=unit ./internal/handler -count=1` | 通过 |
| service、admin handler、DTO、migration 测试 | 通过；全量 unit 中亦通过 |
| 缓存修复回归：`go test -p 1 -tags=unit ./internal/service -run 'CodexWSOnly\|APIKeyService_GetByKey\|AuthCache' -count=1` | 通过；旧 v24 快照回源测试在修复前因返回开关为 false 失败，修复后通过 |
| 缓存修复迁移检查：`go test -p 1 ./migrations -count=1` | 通过 |
| PostgreSQL 18.3（PGlite 0.5.8）补充 SQL 实测 | 对最小关系表执行迁移 184、193、239；修复前开启开关未入队，修复后 23 项通过：默认关闭、开关双向变化/重复保存、原有字段、删除/事务回滚及重复应用迁移；每次入队均核对 SHA256，排除已删除、空密钥和其他分组 |
| `golangci-lint run --new-from-rev=HEAD ./...` | 最终代码 0 issues |
| GroupsView 实际组件测试 | 5/5 通过，含创建 true、默认 false、编辑回显/关闭、平台切换与重置 |
| `npx pnpm@9 typecheck` | 通过 |
| `npx pnpm@9 check:i18n` | 3/3 通过 |
| 前端变更文件 ESLint | 通过 |
| 独立复核 | 数据、UI 和准入通过；Composite compact 边界修复后复核通过 |
| 文件检查 | 49 个变更文件通过 UTF-8、BOM、换行风格复核，`git diff --check` 通过 |

## 真实 Codex CLI

使用本机 Codex CLI 0.153.4 和已有全局配置，仅单次覆盖现有 provider 的 loopback URL 和 `supports_websockets`。未新建 `CODEX_HOME`，未更改全局模型、认证、MCP、插件、权限或 Windows sandbox。

执行 `go test -p 1 -tags=e2e ./internal/server/routes -run TestGatewayCodexWSOnlyCLIStopsAfterHTTP400 -count=1 -v`，两种场景均通过：

| 场景 | WS GET 次数 | HTTP POST 次数 | 最终结果 |
| --- | --- | --- | --- |
| 直接 HTTP | 0 | 1 | 显示策略错误，CLI 退出码 1，无 HTTP 重试 |
| WS 握手返回 426，触发原生回退 | 2 | 1 | 显示相同策略错误，CLI 退出码 1，无 HTTP 重试 |

HTTP 400 由实际网关路由和准入中间件产生；认证分组由测试桩提供，WS 握手失败由测试端模拟。此结果验证客户端接收 400 后停止重试，不表示阻止此前的 WS 重连，也不替代生产数据库及上游账户的完整验收。

## 全量检查的限制

- `go test -p 1 -tags=unit ./...`：除 repository 包外其余包通过。repository 首轮出现 3 个缺少 `sh` 的既有备份测试失败，以及未修改的 `TestAliyunCaptchaVerifier_TransportError` 断言失败。仅为测试进程补充已有 Git `sh` 的 PATH 后，备份测试通过，验证码错误分类测试仍失败。在干净的原始 checkout、相同基线提交上以 `go test -mod=readonly -p 1 -tags=unit ./internal/repository -run '^TestAliyunCaptchaVerifier_TransportError$' -count=1` 单独复现了同一断言失败，确认不由本改动引入。
- 全量 `golangci-lint run ./...`：除已修复的本次测试 CloseNow 漏检查外，余下两项来自未修改的 `internal/service/openai_codex_turn_metadata_test.go:28,47` 类型断言 errcheck；本次增量检查为 0 issues。
- repository 的 migration schema、认证投影以及新增 `TestAuthCacheInvalidationTrigger_CodexWSOnly` 和原有利润字段 integration 测试已尝试执行，测试 harness 检测到本机无 Docker 并跳过。上述 PGlite SQL 实测不替代原生 PostgreSQL、Redis 与 Ent 的完整集成；应在具备 Docker 的环境运行 `go test -p 1 -tags=integration ./internal/repository -count=1`。补充 SQL 工具只放在本地忽略的验证目录，未增加项目依赖或改动测试 harness。
- 未执行部署或生产配置变更。
