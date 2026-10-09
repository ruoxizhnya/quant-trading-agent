# S7-P3-3: Tools Registry — 对外工具提供方架构

> **Task ID**: S7-P3-3
> **Source**: [ODR-043](../odr/odr-043-comprehensive-audit-2026-06-29.md) line 156
> **Status**: Planning
> **Date**: 2026-06-30

---

## 1. 摘要 / Summary

把 quant-trading 服务重构为**对外的工具提供方**：统一暴露 backtest / factor / data-fetch / strategy-registry 四类能力为可发现、可调用的 Tool，并通过 HTTP API（`/api/tools/*`）让**外部 agent**作为消费者调用。

**核心架构方向转变**（来自用户澄清）：
- ❌ 原方向：建内部 Tools Registry 让 AI agent 成为服务内一等居民
- ✅ 新方向：本服务 = 纯粹的 API/工具提供方；agent = 外部消费者（独立服务/repo），通过 HTTP 调用工具并基于响应生成 skills

**本次任务范围**：
1. 建 `pkg/tools/` 包（Tool 接口 + Registry + sentinel errors）
2. 实现 4 个 Tool：BacktestTool / FactorTool / DataFetchTool / StrategyRegistryTool
3. 暴露 HTTP API：`GET /api/tools`（列表+schema）、`GET /api/tools/:name`（单工具 schema）、`POST /api/tools/:name`（执行）
4. 完整测试（每个 Tool + Registry + HTTP handler）
5. 文档更新（SPEC.md + ARCHITECTURE.md）

**本次不做**（留作后续任务）：
- 删除 `pkg/ai/agents/`（破坏性大，单独任务 S7-P3-7）
- 完整 function-calling 循环（agent 侧工作，本服务只提供 API）
- LLM client 扩展（agent 在外部，本服务不需要 LLM 支持工具调用）

---

## 2. 当前状态分析 / Current State

### 2.1 现有 agent 调用模式的痛点

通过 Phase 1 探索发现 4 种调用模式并存：

| 模式 | 位置 | 问题 |
|------|------|------|
| (A) 直接持有 HTTP client | `ValidateAgent.btClient *client.BacktestClient` | 与具体类型耦合，无法替换 |
| (B) 接口注入 | `Pipeline.runner BacktestRunner` | 较好，但每个能力都要单独定义接口 |
| (C) DataProvider 接口 | in-process | 仅进程内，外部无法调用 |
| (D) 纯 LLM prompt | intent/yaml | 无运行时调用 |

**核心问题**：能力散落在各处，没有统一的"发现 + 调用"机制；外部 agent 想调用 backtest 必须自己读 SPEC.md 然后手写 HTTP 调用。

### 2.2 现有 Registry 模式参考

3 个 canonical registry，按演化顺序：

| Registry | 模式 | 特点 | 评价 |
|----------|------|------|------|
| Strategy (`pkg/strategy/registry.go`) | 全局实例 | reject duplicates, GlobalRegister/Get/List | 最老，全局状态 |
| Source (`pkg/data/source/registry.go`) | factory 注入 | allow replacement, sentinel errors, sorted List | **最现代，本次跟随** |
| StockState (`pkg/storage/stockstate_registry.go`) | logger 注入 | — | 不相关 |

### 2.3 现有 HTTP handler 模式

`cmd/analysis/` 下 14 个 `handlers_*.go` 文件，两种模式共存：

- **模式 A**（老）：自由函数 + 闭包，如 `registerStrategyRoutes(router, db)`
- **模式 B**（新，演化方向）：struct + functional options + `RegisterRoutes`，如 `PipelineHandler` / `RiskHandler` / `ExecutionHandler` / `ComplianceHandler`

**本次采用模式 B**，与近期 risk/execution/compliance/pipeline 一致。

### 2.4 现有 client 代码

| 文件 | 状态 | 处理方式 |
|------|------|---------|
| `pkg/ai/client/backtest_client.go` | 被 ValidateAgent L3 使用 | BacktestTool 可复用其 request/response 类型 |
| `pkg/ai/client/factor_client.go` | **死代码**（无 agent 使用） | FactorTool 激活它 |
| `pkg/ai/contracts/contracts.go` | LEAF 包，BacktestRunner 接口 | BacktestTool 委托给它（共存适配器） |

### 2.5 data-service 端点（DataFetchTool 调用目标）

复用 `cmd/analysis/main.go` 顶部已声明的 `httpClient`（带 observability + X-Request-ID 透传），base URL 用 `viper.GetString("data_service.url")`：

| 方法 | 路径 | 用途 |
|------|------|------|
| GET | `/ohlcv/:symbol?start_date=&end_date=` | OHLCV K 线 |
| GET | `/stocks` / `/stocks/:symbol` | 股票元数据 |
| GET | `/fundamentals/:symbol` | 基本面 |
| GET | `/api/v1/trading/calendar?start=&end=` | 交易日历 |

---

## 3. 设计决策 / Design Decisions

### D1. Tool 接口（ISP 拆分）

跟随 `pkg/strategy/interfaces.go` 的 ISP 模式，把 Tool 拆成 3 个单职责子接口 + 1 个复合接口：

```go
// pkg/tools/tool.go
package tools

type ToolCore interface {
    Name() string
    Description() string
}

type SchemaProvider interface {
    // Parameters 返回工具的输入参数 schema（类 JSON Schema 简化版）
    Parameters() []Parameter
    // OutputSchema 返回工具输出的描述（供 agent 理解返回结构）
    OutputSchema() OutputSchema
}

type Executable interface {
    // Execute 调用工具。args 是参数 map，result 是任意可 JSON 序列化的值。
    Execute(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

// Tool 复合接口（向后兼容）
type Tool interface {
    ToolCore
    SchemaProvider
    Executable
}
```

**为什么 ISP 拆分**：
- 允许"只读 schema 不执行"的用例（HTTP GET /api/tools 只调 Name/Description/Parameters）
- 与 `pkg/strategy/interfaces.go` 风格一致
- 类型断言 helper（`AsExecutable`）复用策略包模式

### D2. Registry 结构（跟随 Source Registry 模式）

```go
// pkg/tools/registry.go
type Registry struct {
    mu    sync.RWMutex
    tools map[string]Tool
}

func NewRegistry() *Registry
func (r *Registry) Register(t Tool) error        // allow replacement (hot-reload)
func (r *Registry) Get(name string) (Tool, error) // error if not found
func (r *Registry) List() []ToolInfo              // sorted by Name, 稳定序
func (r *Registry) Execute(ctx, name string, args map[string]interface{}) (interface{}, error)
```

- **不提供全局实例**：用 factory 注入（`ServerDeps.ToolsRegistry`），避免 Strategy Registry 的全局状态陷阱
- **允许同名替换**：支持 hot-reload（跟随 Source Registry，非 Strategy Registry 的 reject 模式）
- **sorted List**：测试稳定性 + API 输出可预测

### D3. Sentinel Errors

```go
// pkg/tools/errors.go
var (
    ErrToolNotRegistered = errors.New("tools: tool not registered")
    ErrEmptyToolName     = errors.New("tools: tool name is empty")
    ErrNilTool           = errors.New("tools: nil tool")
    ErrInvalidArgs       = errors.New("tools: invalid arguments")
)
```

所有错误用 `fmt.Errorf("%w: ...", ErrToolNotRegistered)` 包装，调用方可用 `errors.Is` 区分。

### D4. 四个 Tool 实现策略

| Tool | 实现位置 | 委托目标 | 共存策略 |
|------|---------|---------|---------|
| **BacktestTool** | `pkg/tools/builtin/backtest.go` | `contracts.BacktestRunner` | 共存适配器：BacktestRunner 不变，Tool 内部委托 |
| **FactorTool** | `pkg/tools/builtin/factor.go` | `client.FactorClient` | 激活死代码：复用现有 HTTP client |
| **DataFetchTool** | `pkg/tools/builtin/datafetch.go` | 直接 HTTP（复用 main.go httpClient 模式） | 新建轻量客户端，不引入 pkg/api/ |
| **StrategyRegistryTool** | `pkg/tools/builtin/strategy_registry.go` | `strategy.GlobalList/GlobalGet` | 复用全局注册表（只读） |

**Tool 命名**（kebab-case，HTTP 友好）：
- `backtest.run`
- `factor.compute` / `factor.evaluate`
- `data.ohlcv` / `data.stocks` / `data.fundamentals`
- `strategy.list` / `strategy.get`

**参数 schema 设计**（简化 JSON Schema）：
```go
type Parameter struct {
    Name        string
    Type        string  // "string" | "[]string" | "float" | "int" | "bool"
    Description string
    Required    bool
    Default     interface{}
}

type OutputSchema struct {
    Type        string  // "object" | "array" | "number" | "string"
    Description string
    Fields      []OutputField  // 仅 type=object 时填充
}
```

### D5. HTTP API（模式 B handler）

```go
// cmd/analysis/handlers_tools.go
type ToolsHandler struct {
    registry *tools.Registry
    logger   *log.Logger
}

func NewToolsHandler(reg *tools.Registry, logger *log.Logger) *ToolsHandler
func (h *ToolsHandler) RegisterRoutes(router *gin.Engine)
```

**端点**：

| 方法 | 路径 | 用途 | 响应 |
|------|------|------|------|
| GET | `/api/tools` | 列出所有工具 + schema | `[{name, description, parameters, output_schema}]` |
| GET | `/api/tools/:name` | 单工具详细 schema | `{name, description, parameters, output_schema}` |
| POST | `/api/tools/:name` | 执行工具 | `{result: <任意>, error?: string}` |

**POST 请求体**：
```json
{
  "args": {
    "strategy_name": "momentum",
    "stock_pool": ["000001.SZ"],
    "start_date": "2022-01-01",
    "end_date": "2024-01-01"
  }
}
```

**错误响应**（统一格式）：
```json
{
  "error": "tools: tool not registered: foo.bar",
  "code": "TOOL_NOT_REGISTERED"
}
```

### D6. 依赖注入

`ServerDeps`（`cmd/analysis/setup.go`）新增字段：
```go
type ServerDeps struct {
    // ... existing fields ...
    ToolsRegistry *tools.Registry
}
```

在 `main()` 的 `buildRouter` 之前构造：
```go
toolsReg := tools.NewRegistry()
toolsReg.Register(builtin.NewBacktestTool(deps.Runner))  // 复用现有 BacktestRunner
toolsReg.Register(builtin.NewFactorTool(factorClient))
toolsReg.Register(builtin.NewDataFetchTool(httpClient, dataServiceURL))
toolsReg.Register(builtin.NewStrategyRegistryTool())
deps.ToolsRegistry = toolsReg
```

`registerRoutes()` 末尾追加：
```go
NewToolsHandler(deps.ToolsRegistry, deps.Logger).RegisterRoutes(router)
```

### D7. 包结构

```
pkg/tools/
├── tool.go              # Tool 接口（ISP 拆分）+ Parameter/OutputSchema 类型
├── registry.go          # Registry 结构 + Register/Get/List/Execute
├── errors.go            # sentinel errors
├── helpers.go           # AsExecutable / AsSchemaProvider 类型断言
├── tools_test.go        # Registry + 接口测试
└── builtin/
    ├── backtest.go      # BacktestTool
    ├── backtest_test.go
    ├── factor.go        # FactorTool
    ├── factor_test.go
    ├── datafetch.go     # DataFetchTool
    ├── datafetch_test.go
    ├── strategy_registry.go  # StrategyRegistryTool
    └── strategy_registry_test.go
```

**为什么 `builtin/` 子包**：
- `pkg/tools/` 保持纯净（只有接口 + Registry，无具体实现依赖）
- `builtin/` 是默认提供的工具集，未来可加 `pkg/tools/custom/`
- 避免 `pkg/tools/` 导入 `pkg/ai/client/` / `pkg/strategy/` 造成反向依赖

---

## 4. 提议变更 / Proposed Changes

### Phase 1: pkg/tools/ 基础设施（tool.go + registry.go + errors.go + helpers.go）

**文件**：
- `pkg/tools/tool.go` — Tool 接口（ISP 拆分）+ Parameter/OutputSchema/ToolInfo 类型
- `pkg/tools/registry.go` — Registry 结构 + Register/Get/List/Execute
- `pkg/tools/errors.go` — sentinel errors
- `pkg/tools/helpers.go` — AsExecutable/AsSchemaProvider 类型断言
- `pkg/tools/tools_test.go` — Registry 测试（Register/Get/List/Execute/并发/replacement/nil/empty name）

**测试用例**（test-first）：
- `TestRegistry_RegisterAndGet` — 注册后能 Get 到
- `TestRegistry_Register_Replacement` — 同名替换成功
- `TestRegistry_Register_NilTool` — 返回 ErrNilTool
- `TestRegistry_Register_EmptyName` — 返回 ErrEmptyToolName
- `TestRegistry_Get_NotFound` — 返回 ErrToolNotRegistered
- `TestRegistry_List_Sorted` — 列表按 Name 排序
- `TestRegistry_Execute_Delegates` — Execute 调用 Tool.Execute
- `TestRegistry_Execute_NotFound` — 返回 ErrToolNotRegistered
- `TestRegistry_Concurrent` — 并发 Register/Get 安全（-race）

**Commit 1**: `feat(tools): add Tool interface, Registry, and sentinel errors`
```
pkg/tools/ 基础设施。Tool 接口采用 ISP 拆分（ToolCore + SchemaProvider + Executable），
Registry 跟随 Source Registry 模式（factory 注入、allow replacement、sorted List）。
为后续 4 个 Tool 实现和 HTTP API 暴露奠定基础。

Refs: S7-P3-3
Reviewed: self-review + go vet + gofmt + tests pass
```

### Phase 2: BacktestTool（最关键，验证设计）

**文件**：
- `pkg/tools/builtin/backtest.go` — BacktestTool struct + NewBacktestTool(runner contracts.BacktestRunner)
- `pkg/tools/builtin/backtest_test.go` — 测试

**实现要点**：
- 委托给 `contracts.BacktestRunner`（共存适配器，不破坏现有 agent）
- 参数：`strategy_name` (string, required) / `stock_pool` ([]string, required) / `start_date` (string, required) / `end_date` (string, required)
- 输出：`*domain.BacktestResult`（agent 可直接消费）
- 测试用 mock BacktestRunner（复用 `pkg/ai/pipeline/pipeline_test.go` 的 mock 模式）

**测试用例**：
- `TestBacktestTool_Name` — `backtest.run`
- `TestBacktestTool_Parameters` — 4 个参数 schema 正确
- `TestBacktestTool_Execute_HappyPath` — 委托给 runner，返回 BacktestResult
- `TestBacktestTool_Execute_MissingRequired` — 缺 strategy_name 返回 ErrInvalidArgs
- `TestBacktestTool_Execute_RunnerError` — runner 返回 error 时透传
- `TestBacktestTool_Execute_NilRunner` — 构造时 nil runner panic 或 Execute 返回 error

**Commit 2**: `feat(tools/builtin): add BacktestTool delegating to BacktestRunner`

### Phase 3: FactorTool + DataFetchTool

**文件**：
- `pkg/tools/builtin/factor.go` — FactorTool（委托给 `client.FactorClient`）
- `pkg/tools/builtin/factor_test.go`
- `pkg/tools/builtin/datafetch.go` — DataFetchTool（直接 HTTP 调 data-service）
- `pkg/tools/builtin/datafetch_test.go`

**FactorTool 实现**：
- 两个 action：`factor.compute` / `factor.evaluate`
- 或拆成两个 Tool 实例：`FactorComputeTool` / `FactorEvaluateTool`（更清晰，选这个）
- 参数：`formula` (string, required) / `symbols` ([]string, required) / `start_date` / `end_date`
- 激活 `pkg/ai/client/factor_client.go` 死代码

**DataFetchTool 实现**：
- 三个子工具：`data.ohlcv` / `data.stocks` / `data.fundamentals`
- 复用 main.go httpClient 模式（observability + X-Request-ID）
- base URL 从 `viper.GetString("data_service.url")` 注入
- 测试用 `httptest.NewServer` mock data-service

**Commit 3**: `feat(tools/builtin): add FactorTool (compute + evaluate)`

**Commit 4**: `feat(tools/builtin): add DataFetchTool (ohlcv/stocks/fundamentals)`

### Phase 4: StrategyRegistryTool

**文件**：
- `pkg/tools/builtin/strategy_registry.go`
- `pkg/tools/builtin/strategy_registry_test.go`

**实现**：
- 两个子工具：`strategy.list` / `strategy.get`
- 委托给 `strategy.GlobalList()` / `strategy.GlobalGet(name)`
- 只读，不注册新策略（注册走现有 `/api/strategies` POST）

**Commit 5**: `feat(tools/builtin): add StrategyRegistryTool (list + get)`

### Phase 5: HTTP handler + 路由注册

**文件**：
- `cmd/analysis/handlers_tools.go` — ToolsHandler struct + RegisterRoutes
- `cmd/analysis/handlers_tools_test.go` — HTTP 测试
- `cmd/analysis/setup.go` — ServerDeps 增加 ToolsRegistry 字段
- `cmd/analysis/main.go` — 构造 Registry + 注册 4 个 tool + 调用 RegisterRoutes

**测试用例**：
- `TestHandler_ListTools` — GET /api/tools 返回所有工具
- `TestHandler_GetTool` — GET /api/tools/backtest.run 返回单工具 schema
- `TestHandler_GetTool_NotFound` — 404
- `TestHandler_ExecuteTool` — POST /api/tools/backtest.run 执行成功
- `TestHandler_ExecuteTool_NotFound` — 404
- `TestHandler_ExecuteTool_InvalidArgs` — 400
- `TestHandler_ExecuteTool_ToolError` — 500 + error message

**Commit 6**: `feat(analysis): add /api/tools HTTP endpoints + wire ToolsRegistry into ServerDeps`

### Phase 6: 文档更新 + 标记任务完成

**文件**：
- `docs/SPEC.md` — 新增 "Tools API" 章节（GET /api/tools, GET /api/tools/:name, POST /api/tools/:name）
- `docs/ARCHITECTURE.md` — 新增 Tools Registry 架构说明（pkg/tools/ 包结构 + 4 个 Tool + HTTP 暴露）
- `docs/TASKS.md` — S7-P3-3 状态 `⬜` → `✅`

**Commit 7**: `docs: document Tools Registry API and architecture (S7-P3-3)`

---

## 5. 假设与决策 / Assumptions & Decisions

### 假设
1. **agent 移除是后续任务**：本次只建工具提供方，不删除 `pkg/ai/agents/`。新代码不依赖 agent 包。
2. **BacktestRunner 共存**：BacktestTool 委托给 BacktestRunner，现有 agent 不动。等 agent 移除后再决定是否删除 BacktestRunner。
3. **不引入 pkg/api/**：DataFetchTool 内联 HTTP 客户端，避免本次任务膨胀。若未来多个 tool 都需要 data-service 客户端，再抽 `pkg/api/dataservice/`。
4. **Tool 命名用 kebab-case + 点分**：`backtest.run` / `factor.compute`，HTTP 友好且语义清晰。
5. **不实现 function-calling 循环**：本服务只暴露 API，agent 侧的 LLM tools 字段、tool_calls 处理由外部 agent 服务实现。

### 关键决策汇总

| # | 决策 | 理由 |
|---|------|------|
| D1 | Tool 接口 ISP 拆分（ToolCore + SchemaProvider + Executable） | 与 strategy/interfaces.go 一致；支持"只读 schema 不执行"用例 |
| D2 | Registry 跟随 Source Registry 模式（factory 注入、allow replacement、sorted List） | 最现代的 registry 模式；避免全局状态；支持 hot-reload |
| D3 | Sentinel errors + errors.Is | 调用方可区分"未注册"vs"执行失败"vs"参数错误" |
| D4 | 4 个 Tool 分别委托给 BacktestRunner / FactorClient / HTTP / strategy.GlobalList | 共存适配器模式，零破坏性 |
| D5 | HTTP handler 用模式 B（struct + RegisterRoutes） | 与 risk/execution/compliance/pipeline 一致 |
| D6 | ServerDeps 注入 ToolsRegistry | factory 模式，避免全局实例 |
| D7 | builtin/ 子包隔离具体实现 | pkg/tools/ 保持纯净，无反向依赖 |

---

## 6. 验证步骤 / Verification

### 提交前检查清单（每个 commit）

- [ ] `go build ./...` 通过
- [ ] `go vet ./...` 无警告
- [ ] `gofmt -l .` 无输出
- [ ] `go test ./pkg/tools/... -count=1 -race` 通过
- [ ] commit message 符合格式

### 最终验证（Phase 6 后）

- [ ] `go test ./... -count=1 -race`（excl. e2e）通过
- [ ] `curl http://localhost:8085/api/tools` 返回 4 个工具
- [ ] `curl http://localhost:8085/api/tools/backtest.run` 返回 schema
- [ ] `curl -X POST http://localhost:8085/api/tools/backtest.run -d '{"args":{...}}'` 执行成功
- [ ] SPEC.md / ARCHITECTURE.md 更新
- [ ] TASKS.md 标记 S7-P3-3 完成

### 测试质量要求（per AGENTS.md §8.3 规范 1）

- 必须有行为断言（禁止 `assert.True(t, true)`）
- 必须覆盖边界条件（空输入、非法输入、并发场景）
- 禁止 `time.Sleep` 同步并发测试
- 测试名描述被测行为（如 `TestBacktestTool_Execute_MissingRequired`）
- `-count=2` 安全（避免全局状态泄漏）

---

## 7. 风险与缓解 / Risks

| 风险 | 缓解 |
|------|------|
| Tool 接口设计过度通用化导致 4 个 Tool 实现别扭 | Phase 2 先实现 BacktestTool 验证接口可用性；若发现接口不适合，立即调整而非硬塞 |
| DataFetchTool 直接 HTTP 调用重复 handlers_proxy.go 逻辑 | 本次接受重复；未来抽 `pkg/api/dataservice/` 时统一 |
| 参数 map[string]interface{} 类型弱 | Parameter schema 提供类型信息；Execute 内部做强类型转换 + ErrInvalidArgs |
| 全局 strategy.Registry 与新 Tools Registry 概念重叠 | StrategyRegistryTool 只读封装 GlobalList/GlobalGet，不引入新注册表 |

---

## 8. 文件清单 / File Manifest

### 新增文件（11 个）

```
pkg/tools/tool.go
pkg/tools/registry.go
pkg/tools/errors.go
pkg/tools/helpers.go
pkg/tools/tools_test.go
pkg/tools/builtin/backtest.go
pkg/tools/builtin/backtest_test.go
pkg/tools/builtin/factor.go
pkg/tools/builtin/factor_test.go
pkg/tools/builtin/datafetch.go
pkg/tools/builtin/datafetch_test.go
pkg/tools/builtin/strategy_registry.go
pkg/tools/builtin/strategy_registry_test.go
cmd/analysis/handlers_tools.go
cmd/analysis/handlers_tools_test.go
```

### 修改文件（4 个）

```
cmd/analysis/setup.go        — ServerDeps 增加 ToolsRegistry 字段
cmd/analysis/main.go         — 构造 Registry + 注册 4 个 tool + 调用 RegisterRoutes
docs/SPEC.md                 — 新增 Tools API 章节
docs/ARCHITECTURE.md         — 新增 Tools Registry 架构说明
docs/TASKS.md                — S7-P3-3 状态标记完成
```

### 预计 commit 数：7

---

## 9. 后续任务 / Next Steps

本次任务完成后，建议的后续任务（不在 S7-P3-3 范围内）：

- **S7-P3-7**: 移除 `pkg/ai/agents/` 到独立 repo（破坏性变更，需单独 ODR）
- **S7-P3-8**: 扩展 Tool 集（portfolio analysis / walk-forward / batch backtest 等）
- **S7-P3-9**: 抽 `pkg/api/dataservice/` 统一 data-service 客户端
- **外部 agent 服务**: 基于 `/api/tools` 自动生成 skills（LLM function-calling）
