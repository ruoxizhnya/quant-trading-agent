# S7-P3-3 收尾计划：Phase 5 + Phase 6

> **Task ID**: S7-P3-3
> **Parent Plan**: [.trae/documents/s7-p3-3-tools-registry.md](s7-p3-3-tools-registry.md)（已批准，6 阶段 7 提交）
> **Status**: Phase 1–4 已提交（5 commits on `feat/s7-p3-3-tools-registry`），Phase 5 半完成，Phase 6 未开始
> **Date**: 2026-06-30

---

## 1. 摘要 / Summary

S7-P3-3 的完整计划已在 `s7-p3-3-tools-registry.md` 中批准并执行到 Phase 4。当前状态：

- ✅ **Phase 1–4 已提交**（5 个 commit）：`pkg/tools/` 基础设施 + 4 个 builtin Tool（backtest/factor/datafetch/strategy_registry）全部完成，72+ 测试通过
- 🔧 **Phase 5 半完成**（未提交）：`handlers_tools.go` 已创建，`deps.go`/`setup.go`/`main.go` 已修改注入 `ToolsRegistry`，但**缺少两步**：
  1. `registerRoutes()` 未调用 `NewToolsHandler(...).RegisterRoutes(router)`
  2. `handlers_tools_test.go` 未创建
- ⬜ **Phase 6 未开始**：文档更新 + 任务标记

本计划仅覆盖**剩余的 Phase 5 收尾 + Phase 6 文档**，不重新规划已批准的内容。

---

## 2. 当前状态确认 / Current State (Phase 1 探索验证)

### 2.1 Git 状态

```
分支: feat/s7-p3-3-tools-registry
已提交 (5):
  d4ce85a feat(tools/builtin): add StrategyRegistryTool (list + get)
  bee0283 feat(tools/builtin): add DataFetchTool (ohlcv/stocks/fundamentals)
  3887566 feat(tools/builtin): add FactorTool (compute + evaluate)
  b60afac feat(tools/builtin): add BacktestTool delegating to BacktestRunner
  5fae812 feat(tools): add Tool interface, Registry, and sentinel errors

未提交变更 (5 文件 modified + 1 文件 untracked):
  modified:   cmd/analysis/deps.go            (已加 ToolsRegistry 字段)
  modified:   cmd/analysis/main.go            (已加 buildToolsRegistry 调用 + ToolsRegistry 注入 deps)
  modified:   cmd/analysis/setup.go           (已加 buildToolsRegistry 函数 + imports)
  modified:   pkg/tools/builtin/datafetch.go  (导出 DataSourceClient/NewDataSourceClient)
  modified:   pkg/tools/builtin/datafetch_test.go  (跟随导出名)
  untracked:  cmd/analysis/handlers_tools.go  (ToolsHandler struct 已完成)
```

### 2.2 已验证的文件内容

- **`cmd/analysis/handlers_tools.go`**（已创建，162 行）：ToolsHandler struct + 3 端点（list/get/execute）+ 错误分类（ErrToolNotRegistered→404, ErrInvalidArgs→400, 其他→500）。代码完整，无需修改。
- **`cmd/analysis/setup.go:349-415`**（`buildToolsRegistry` 函数）：已注册全部 8 个 tool 实例（backtest.run + factor.compute/evaluate + data.ohlcv/stocks/fundamentals + strategy.list/get）。代码完整。
- **`cmd/analysis/deps.go`**（`ToolsRegistry *tools.Registry` 字段）：已加到 ServerDeps struct。
- **`cmd/analysis/main.go`**：第 144 行 `toolsRegistry := buildToolsRegistry(...)`，第 163 行 `ToolsRegistry: toolsRegistry` 注入 deps。**但 `registerRoutes()` 函数（第 195-318 行）未调用 `NewToolsHandler`**。

### 2.3 测试模式参考

`cmd/analysis/handlers_compliance_test.go` 提供了 pattern B handler 的标准测试模式：
- `gin.SetMode(gin.TestMode)` 在 `init()` 中设置
- `doRequest(handler, method, path, body)` helper：新建 `gin.New()` router → `handler.RegisterRoutes(r)` → `httptest.NewRecorder` → 返回 `*httptest.ResponseRecorder`
- 每个测试用例独立构造 handler，断言 status code + body 内容

---

## 3. 提议变更 / Proposed Changes

### Phase 5 收尾（Commit 6）

#### 3.1 修改 `cmd/analysis/main.go` — 注册路由

**位置**：`registerRoutes()` 函数末尾，第 317 行 `NewComplianceHandler(...).RegisterRoutes(router)` 之后、第 318 行 `}` 之前。

**新增 1 行**（带注释，3 行）：

```go
	// S7-P3-3 (ODR-043): Tools Registry endpoints. Exposes backtest /
	// factor / data / strategy capabilities as discoverable Tools over
	// /api/tools/* so external agent services can call without reading
	// SPEC.md.
	NewToolsHandler(deps.ToolsRegistry, deps.Logger).RegisterRoutes(router)
```

**Why**：当前 `ToolsRegistry` 已构造并注入 `deps`，但 HTTP 端点未挂载，外部无法访问。这一行完成"工具提供方"架构的最后一公里。

#### 3.2 创建 `cmd/analysis/handlers_tools_test.go` — HTTP 测试

**模式**：跟随 `handlers_compliance_test.go` 的 `doRequest` helper 模式。

**测试用例**（7 个，覆盖 3 端点 + 错误分类）：

| 测试名 | 端点 | 断言 |
|--------|------|------|
| `TestHandler_Tools_ListTools` | `GET /api/tools` | 200 + `tools` 数组 + `count` 字段 |
| `TestHandler_Tools_ListTools_Empty` | `GET /api/tools` | 200 + count=0（空 registry） |
| `TestHandler_Tools_GetTool` | `GET /api/tools/backtest.run` | 200 + ToolInfo 含 parameters + output_schema |
| `TestHandler_Tools_GetTool_NotFound` | `GET /api/tools/nonexistent` | 404 + `TOOL_NOT_REGISTERED` code |
| `TestHandler_Tools_ExecuteTool_HappyPath` | `POST /api/tools/echo` | 200 + `result` 字段（用 fakeTool） |
| `TestHandler_Tools_ExecuteTool_NotFound` | `POST /api/tools/nonexistent` | 404 |
| `TestHandler_Tools_ExecuteTool_InvalidArgs` | `POST /api/tools/strict` (返回 ErrInvalidArgs 的 fake) | 400 + `INVALID_ARGS` code |
| `TestHandler_Tools_ExecuteTool_ToolError` | `POST /api/tools/failing` (返回普通 error 的 fake) | 500 + `TOOL_EXECUTION_FAILED` code |
| `TestHandler_Tools_ExecuteTool_InvalidBody` | `POST /api/tools/echo` (body 非 JSON) | 400 + `INVALID_BODY` code |
| `TestHandler_Tools_NilRegistry_Panics` | 构造 `NewToolsHandler(nil, ...)` | `panic` (用 `recover` 断言) |

**fakeTool 测试夹具**（定义在测试文件内）：
```go
// echoTool 回显 args["msg"]，用于 happy-path 测试
type echoTool struct{}
func (echoTool) Name() string { return "echo" }
func (echoTool) Description() string { return "echoes args" }
func (echoTool) Parameters() []tools.Parameter { return nil }
func (echoTool) OutputSchema() tools.OutputSchema { return tools.OutputSchema{Type: "string"} }
func (echoTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
    return args["msg"], nil
}

// strictTool 返回 ErrInvalidArgs
// failingTool 返回普通 error
```

**Why**：覆盖 3 个端点的 happy path + 全部 4 种错误分类（not_registered / invalid_args / tool_error / invalid_body），确保 HTTP 状态码映射正确。

#### 3.3 验证 + 提交

```bash
go build ./cmd/analysis/...
go vet ./cmd/analysis/...
gofmt -w cmd/analysis/handlers_tools.go cmd/analysis/handlers_tools_test.go cmd/analysis/main.go
go test ./cmd/analysis/... -count=1 -race
git add cmd/analysis/handlers_tools.go cmd/analysis/handlers_tools_test.go \
        cmd/analysis/main.go cmd/analysis/deps.go cmd/analysis/setup.go \
        pkg/tools/builtin/datafetch.go pkg/tools/builtin/datafetch_test.go
# 注意：handlers_tools_test.go 是新文件，需要 git add -f（.gitignore 屏蔽 *_test.go）
git add -f cmd/analysis/handlers_tools_test.go
git commit -m "feat(analysis): add /api/tools HTTP endpoints + wire ToolsRegistry into ServerDeps"
```

**Commit message**:
```
feat(analysis): add /api/tools HTTP endpoints + wire ToolsRegistry into ServerDeps

Phase 5 of S7-P3-3: expose the Tools Registry over HTTP so external
agent services can discover and invoke platform capabilities via
GET /api/tools, GET /api/tools/:name, POST /api/tools/:name. ToolsHandler
follows the pattern-B handler style (struct + RegisterRoutes) used by
RiskHandler/ExecutionHandler/ComplianceHandler. ServerDeps now carries
a ToolsRegistry field wired in main() via buildToolsRegistry, which
registers all 8 builtin tools (backtest/factor/data/strategy). Also
exports DataSourceClient/NewDataSourceClient in datafetch.go so main.go
can construct DataFetch tools.

Refs: S7-P3-3
Reviewed: self-review + go vet + gofmt + tests pass
```

---

### Phase 6（Commit 7）— 文档更新

#### 3.4 更新 `docs/ARCHITECTURE.md`

**位置 1**：`## API 端点 → ### Analysis Service (8085)` 代码块末尾（第 281 行 `# Legacy HTML` 之前）追加：

```
# Tools Registry (S7-P3-3, ODR-043) — 对外工具提供方
GET  /api/tools              — 列出所有工具 + schema
GET  /api/tools/:name        — 单工具 schema
POST /api/tools/:name        — 执行工具 (body: {"args": {...}})
```

**位置 2**：在 `## AI 研究架构 (pkg/ai/) — Phase 4` 章节之前（第 814 行之前）新增一节：

```markdown
## Tools Registry 架构 (pkg/tools/) — S7-P3-3

> **设计方向**: 本服务 = 对外的 API/工具提供方；agent = 外部消费者，
> 通过 HTTP 调用 `/api/tools/*` 发现并执行工具，基于响应生成 skills。

### 包结构

\`\`\`
pkg/tools/
├── tool.go              # Tool 接口（ISP 拆分：ToolCore + SchemaProvider + Executable）
├── registry.go          # Registry（factory 注入、allow replacement、sorted List）
├── errors.go            # sentinel errors (ErrToolNotRegistered / ErrInvalidArgs / ...)
├── helpers.go           # AsExecutable / AsSchemaProvider 类型断言
└── builtin/
    ├── backtest.go           # backtest.run → contracts.BacktestRunner
    ├── factor.go             # factor.compute / factor.evaluate → client.FactorClient
    ├── datafetch.go          # data.ohlcv / data.stocks / data.fundamentals → data-service HTTP
    └── strategy_registry.go  # strategy.list / strategy.get → strategy.GlobalList/Get
\`\`\`

### 设计要点

- **共存适配器**: BacktestTool 委托给现有 `contracts.BacktestRunner`，不破坏现有 agent
- **factory 注入**: Registry 通过 `ServerDeps.ToolsRegistry` 注入，无全局实例
- **builtin/ 子包隔离**: `pkg/tools/` 保持纯净（只有接口），具体实现依赖在 `builtin/`
- **8 个 builtin tool**: backtest.run, factor.compute, factor.evaluate, data.ohlcv, data.stocks, data.fundamentals, strategy.list, strategy.get
```

#### 3.5 更新 `docs/SPEC.md`

**位置**：在 `## Microservices` 章节之前（第 624 行之前）新增一节 `## Tools API (S7-P3-3)`：

```markdown
## Tools API (S7-P3-3)

> 架构方向：本服务作为对外的工具提供方，外部 agent 通过 HTTP 调用工具。

### GET /api/tools

列出所有已注册工具及其 schema。

**响应**:
\`\`\`json
{
  "tools": [
    {
      "name": "backtest.run",
      "description": "Run a backtest for a strategy",
      "parameters": [...],
      "output_schema": {...}
    }
  ],
  "count": 8
}
\`\`\`

### GET /api/tools/:name

获取单个工具的 schema。

**响应**: `ToolInfo` 对象（name + description + parameters + output_schema）

**错误**:
- `404` `{"error": "...", "code": "TOOL_NOT_REGISTERED"}`

### POST /api/tools/:name

执行工具。

**请求体**:
\`\`\`json
{
  "args": {
    "strategy_name": "momentum",
    "stock_pool": ["000001.SZ"],
    "start_date": "2022-01-01",
    "end_date": "2024-01-01"
  }
}
\`\`\`

**响应**: `{"result": <任意 JSON 值>}`

**错误**:
- `404` `TOOL_NOT_REGISTERED` — 工具名未注册
- `400` `INVALID_ARGS` — 参数缺失或类型错误
- `400` `INVALID_BODY` — 请求体非合法 JSON
- `500` `TOOL_EXECUTION_FAILED` — 工具执行失败

### 内置工具

| 工具名 | 参数 | 输出 |
|--------|------|------|
| `backtest.run` | strategy_name, stock_pool, start_date, end_date | BacktestResult |
| `factor.compute` | formula, symbols, start_date, end_date | map[string][]float64 |
| `factor.evaluate` | formula, symbols, start_date, end_date | FactorMetrics |
| `data.ohlcv` | symbol, start_date, end_date | []OHLCV |
| `data.stocks` | symbol? | []Stock |
| `data.fundamentals` | symbol | []Fundamental |
| `strategy.list` | (无) | []StrategyInfo |
| `strategy.get` | name | StrategyInfo |
```

#### 3.6 更新 `docs/TASKS.md`

**位置**：第 1629 行。

**修改**：
```
| S7-P3-3 | 建 pkg/tools/registry.go Tools Registry（AI 一等居民的最后一公里） | `pkg/tools/` 新建 | ⬜ | ODR-043 |
```
→
```
| S7-P3-3 | 建 pkg/tools/registry.go Tools Registry（对外工具提供方：pkg/tools + 4 builtin Tool + /api/tools HTTP API） | `pkg/tools/`, `cmd/analysis/handlers_tools.go` | ✅ | ODR-043 |
```

（同时微调描述以反映实际实现方向"对外工具提供方"而非"AI 一等居民"）

#### 3.7 提交 Phase 6

```bash
git add docs/ARCHITECTURE.md docs/SPEC.md docs/TASKS.md
git commit -m "docs: document Tools Registry API and architecture (S7-P3-3)"
```

**Commit message**:
```
docs: document Tools Registry API and architecture (S7-P3-3)

Phase 6 of S7-P3-3: add Tools API section to SPEC.md (3 endpoints +
8 builtin tools), add Tools Registry architecture section to
ARCHITECTURE.md (package structure + design points + /api/tools/*
endpoints), mark S7-P3-3 complete in TASKS.md. Description updated
to reflect the "对外工具提供方" direction (service = API provider,
agent = external consumer).

Refs: S7-P3-3
Reviewed: self-review + cross-reference check
```

---

## 4. 最终验证 / Final Verification

Phase 6 提交后运行：

```bash
# 全量构建 + 静态检查
go build ./...
go vet ./...
gofmt -l . | grep -v _test.go

# 全量测试（excl. e2e）
go test ./... -count=1 -race

# 验证 /api/tools 端点（需先启动服务，可选）
# curl http://localhost:8085/api/tools | jq .
# curl http://localhost:8085/api/tools/backtest.run | jq .
```

**验证清单**：
- [ ] `go build ./...` 通过
- [ ] `go vet ./...` 无警告
- [ ] `gofmt -l .` 无输出（除 _test.go）
- [ ] `go test ./cmd/analysis/... -count=1 -race` 通过（含新 handlers_tools_test.go）
- [ ] `go test ./pkg/tools/... -count=1 -race` 通过（回归）
- [ ] `go test ./... -count=1 -race`（excl. e2e）通过
- [ ] SPEC.md / ARCHITECTURE.md / TASKS.md 更新
- [ ] 2 个 commit（Phase 5 + Phase 6）符合 atomic commit 规范

---

## 5. 假设与决策

1. **不重新规划已批准内容**：本计划仅覆盖 Phase 5 收尾 + Phase 6，不复盘 Phase 1–4 的设计决策（已在 `s7-p3-3-tools-registry.md` 中确定）。
2. **handlers_tools_test.go 用 fakeTool 而非真实 builtin**：HTTP handler 测试聚焦路由 + 错误分类，不依赖 backtest/factor/data-service 的真实依赖。builtin tool 自身的正确性已由 `pkg/tools/builtin/*_test.go` 覆盖。
3. **git add -f handlers_tools_test.go**：项目 `.gitignore` 第 28 行屏蔽 `*_test.go`，新测试文件需 force-add（per project memory 记录的约定）。
4. **不修改 `handlers_tools.go`**：已验证其内容完整正确（3 端点 + 错误分类 + pattern B），无需调整。
5. **TASKS.md 描述微调**：原描述"AI 一等居民的最后一公里"与最终实现方向（对外工具提供方）不符，更新为更准确的描述。

---

## 6. 执行顺序 / Execution Order

1. 修改 `cmd/analysis/main.go` — 加 1 行路由注册（Edit 工具）
2. 创建 `cmd/analysis/handlers_tools_test.go` — 10 个测试用例（Write 工具）
3. 运行 `go build ./cmd/analysis/... && go vet && gofmt -w && go test -race`
4. `git add` + `git add -f` 暂存全部 Phase 5 变更
5. Commit Phase 5
6. 更新 `docs/ARCHITECTURE.md`（2 处：API 端点 + 新章节）
7. 更新 `docs/SPEC.md`（新增 Tools API 章节）
8. 更新 `docs/TASKS.md`（标记完成 + 描述微调）
9. Commit Phase 6
10. 运行最终验证（全量 build/vet/test）
11. 返回最终响应
