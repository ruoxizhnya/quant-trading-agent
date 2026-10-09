# S7-P3-2: YAML → ExpressionStrategy 加载器

> **Task ID**: S7-P3-2 (ODR-043 Sprint 7 P3)
> **Branch**: `feat/s7-p3-2-yaml-expression-loader`
> **Depends on**: S7-P3-1 (已完成, merged 097d1d2)

## Context

S7-P3-1 建成了 `pkg/strategy/expression/` 包，让 DSL 表达式能作为 `strategy.Strategy` 在回测引擎中运行。但当前 AI pipeline 仍然走重路径：

```
Intent → Generator → YAML (仅人类阅读) → LLM 生成 Go 代码 → go build 编译 → 回测
```

S7-P3-2 的目标是让 YAML 成为**可执行产物**，对表达式类策略绕过 LLM codegen + 编译步骤：

```
Intent → Generator → YAML → Loader → ExpressionStrategy → GlobalRegister → 回测
```

这带来三个收益：
1. **零 LLM 调用**：简单表达式策略不需要 LLM 生成 Go 代码
2. **零编译子进程**：不需要 `go build`，更快更安全（无沙箱顾虑）
3. **AI 一等居民**：AI 输出 YAML 即可执行，降低 AI 策略生成门槛

## 设计决策

### D1: 扩展现有 `Config` 结构，新增 `expression` section

在 `pkg/ai/yaml/generator.go` 的 `Config` 中加 `Expression ExpressionYAML`，子结构 `SignalYAML`/`SizingYAML`/`RiskYAML` 1:1 映射到 `expression.ExpressionStrategyConfig`。

YAML 格式：
```yaml
strategy:
  name: my_expr_strat
  type: expression
  description: ...
expression:
  signal:
    expression: "cs_rank(close) > 0.8"
    action: buy
    direction: long
    min_strength: 0
    lookback: 60
  sizing:
    method: equal
    fixed_weight: 0.05
    max_per_stock: 0.10
    max_total: 1.0
  risk:
    max_position_pct: 0.10
    max_open_positions: 20
    min_cash_buffer: 0.05
backtest:
  start_date: 2022-01-01
  ...
```

**注意**：顶层 `risk:` (max_positions/stop_loss) 是回测引擎级风控；`expression.risk:` 是信号后权重风控。两者语义不同，文档用表格区分。

### D2: Loader 为包级函数（非 `*Generator` 方法）

`ParseConfig` / `LoadStrategy` 不需要 Generator 的 `indentSize` 状态，作为包级函数更清晰：
- `yaml.ParseConfig(yamlStr) (*Config, error)` — 用 yaml.v3 解析
- `yaml.LoadStrategy(yamlStr) (strategy.Strategy, error)` — 解析 + 构造 ExpressionStrategy
- `yaml.LoadAndRegister(yamlStr) (strategy.Strategy, error)` — 便捷糖，Load + GlobalRegister

### D3: 检测逻辑

`LoadStrategy` 判定优先级：
1. `expression:` section 存在且非空 → 用其值构造 ExpressionStrategy
2. `strategy.type == "expression"` 但无 `expression:` section → 用 `defaultExpressionStrategyConfig()` 构造
3. 否则 → error: `LoadStrategy only supports expression-type strategies`

### D4: 边界处理

- **空 name**：校验 `config.Strategy.Name != ""`，否则清晰 error
- **`expression_template` 名**：拒绝（与 init() 自注册冲突），提示用户改名
- **Direction 字符串**：`domain.Direction(s)` 直接转换 + switch 校验 long/short/close
- **注册冲突**：`ExecuteFromYAML` 先 `GlobalGet(name)`，若存在且是 `*expression.ExpressionStrategy` 则 `Configure(params)` 覆盖；若存在但类型不同则 error；若不存在则 `GlobalRegister`
- **不自动注册**：`LoadStrategy` 只返回对象，注册留给 caller（单一职责 + 测试隔离）

### D5: yaml.v3 依赖提升

`gopkg.in/yaml.v3 v3.0.1` 已在 go.mod 作为 indirect dep（被 testify 拉入）。本任务首次在业务代码中 import 它，`go mod tidy` 会自动提升为 direct dep。import 别名用 `yamlv3` 避免与本包名 `yaml` 混淆。

## 实施阶段（5 个原子 commit）

### Phase 1: Schema 类型 + ParseConfig + 测试

**文件**:
- 修改 `pkg/ai/yaml/generator.go`：加 `ExpressionYAML`/`SignalYAML`/`SizingYAML`/`RiskYAML` 类型 + `Config.Expression` 字段
- 新建 `pkg/ai/yaml/loader.go`：`ParseConfig` 函数（用 yaml.v3.Unmarshal）
- 新建 `pkg/ai/yaml/loader_test.go`：5 个测试

**测试**:
1. `TestParseConfig_ValidFullYAML` — 完整 YAML（含 expression section）→ 字段匹配
2. `TestParseConfig_EmptyString` — 空字符串 → error
3. `TestParseConfig_MalformedYAML` — 语法错误 → error
4. `TestParseConfig_MissingStrategySection` — 无 `strategy:` → error
5. `TestParseConfig_NoExpressionSection` — 有 strategy/backtest 但无 expression → 解析成功（Expression 零值）

**Commit**: `feat(ai/yaml): add ExpressionYAML schema and ParseConfig`

### Phase 2: LoadStrategy + 测试

**文件**:
- 修改 `pkg/ai/yaml/loader.go`：加 `LoadStrategy` + `LoadAndRegister`
- 修改 `pkg/ai/yaml/loader_test.go`：加 7 个测试

**测试**:
6. `TestLoadStrategy_HappyPath` — 完整 YAML → 返回 `*ExpressionStrategy`，Name/参数匹配
7. `TestLoadStrategy_DefaultsApplied` — `type=expression` 但无 expression section → 用默认 config
8. `TestLoadStrategy_InvalidDirection` — `direction: sideways` → error
9. `TestLoadStrategy_InvalidSizingMethod` — `method: martingale` → error
10. `TestLoadStrategy_InvalidExpression` — `expression: "cs_rank(close >"` → error
11. `TestLoadStrategy_EmptyName` — `name: ""` → error
12. `TestLoadStrategy_RejectsExpressionTemplateName` — `name: expression_template` → error

**文档同步**: `docs/SPEC.md` 更新 ExpressionStrategy YAML schema 表格

**Commit**: `feat(ai/yaml): add LoadStrategy for YAML→ExpressionStrategy`

### Phase 3: Generator 扩展输出 expression section + round-trip

**文件**:
- 修改 `pkg/ai/yaml/generator.go`：扩展 `intentToConfig`（intent 含 `signal_expr` 参数时填充 Expression）+ `configToYAML`（输出 `expression:` section，字符串字段用双引号）
- 修改 `pkg/ai/yaml/generator_test.go`：加 3 个测试

**测试**:
13. `TestGenerator_Generate_EmitsExpressionSection` — intent 含 `signal_expr` → 输出含 `expression:` section
14. `TestGenerator_Generate_ExpressionWithSpecialChars` — expression 含冒号 → round-trip 可解析
15. `TestGenerator_Generate_NoExpressionParams` — intent 无 `signal_expr` → 不输出 `expression:` section（向后兼容）

**Commit**: `feat(ai/yaml): emit expression section from intent parameters`

### Phase 4: Pipeline.ExecuteFromYAML + 测试

**文件**:
- 修改 `pkg/ai/pipeline/pipeline.go`：加 `ExecuteFromYAML(ctx, yamlStr, runner) (*Result, error)`
- 修改 `pkg/ai/pipeline/pipeline_test.go`：加 3 个测试

**ExecuteFromYAML 流程**:
1. `yaml.LoadStrategy(yamlStr)` → strategy
2. 注册冲突处理：`GlobalGet(name)` → 若存在且同类型则 `Configure`；若不同类型则 error；若不存在则 `GlobalRegister`
3. `runner.RunBacktest(ctx, name, universe, startDate, endDate)` — universe/dates 从 YAML 的 backtest/data section 提取
4. 填充 `Result.YAMLConfig` / `Result.BacktestResult`，跳过 `GeneratedCode`/`BuildError`

**测试**:
16. `TestPipeline_ExecuteFromYAML_HappyPath` — mock runner，验证 BacktestResult 返回
17. `TestPipeline_ExecuteFromYAML_RegistrationCollision` — 同名 ExpressionStrategy 已注册 → Configure 覆盖
18. `TestPipeline_ExecuteFromYAML_InvalidYAML` — 坏 YAML → Result.Status == StageFailed

**文档同步**: `docs/SPEC.md` 更新 AI Pipeline Integration 节；`docs/ARCHITECTURE.md` 加 YAML 直执行路径

**Commit**: `feat(ai/pipeline): add ExecuteFromYAML bypassing codegen`

### Phase 5: 任务状态更新

**文件**: `docs/TASKS.md` — S7-P3-2 行 ⬜ → ✅

**Commit**: `chore(docs): mark S7-P3-2 complete`

## 关键文件清单

| 文件 | 操作 | Phase |
|------|------|-------|
| `pkg/ai/yaml/generator.go` | 修改（加类型 + 扩展 Generate） | 1, 3 |
| `pkg/ai/yaml/loader.go` | 新建 | 1, 2 |
| `pkg/ai/yaml/loader_test.go` | 新建 | 1, 2 |
| `pkg/ai/yaml/generator_test.go` | 修改（加 3 测试） | 3 |
| `pkg/ai/pipeline/pipeline.go` | 修改（加 ExecuteFromYAML） | 4 |
| `pkg/ai/pipeline/pipeline_test.go` | 修改（加 3 测试） | 4 |
| `go.mod` | 自动（go mod tidy 提升 yaml.v3） | 1 |
| `docs/SPEC.md` | 修改 | 2, 4 |
| `docs/ARCHITECTURE.md` | 修改 | 4 |
| `docs/TASKS.md` | 修改 | 5 |

**不修改**：`pkg/strategy/expression/` 下任何文件（S7-P3-1 的 API 已满足 loader 需求）

## 复用的现有 API

- `expression.NewExpressionStrategy(name, cfg) (*ExpressionStrategy, error)` — 构造器
- `expression.ExpressionStrategyConfig{SignalCfg, SizingCfg, RiskCfg}` — 配置结构
- `expression.SignalConfig{Expression, Action, Direction, MinStrength, Lookback}`
- `expression.SizingConfig{Method, FixedWeight, MaxPerStock, MaxTotal}`
- `expression.RiskConfig{MaxPositionPct, MaxDrawdown, MaxOpenPositions, MinCashBuffer}`
- `strategy.GlobalRegister(s) error` / `strategy.GlobalGet(name) (Strategy, error)`
- `strategy.AsConfigurable(s).Configure(params)` — 注册冲突时覆盖参数
- `domain.Direction` 字符串常量（`DirectionLong="long"` 等，可直接 `domain.Direction(s)` 转换）
- `contracts.BacktestRunner.RunBacktest(ctx, name, pool, start, end)` — pipeline 集成点

## 验证方法

### 每个 Phase 验证
```bash
go build ./pkg/ai/yaml/... ./pkg/ai/pipeline/...
go vet ./pkg/ai/yaml/... ./pkg/ai/pipeline/...
gofmt -l pkg/ai/yaml/ pkg/ai/pipeline/
go test ./pkg/ai/yaml/... ./pkg/ai/pipeline/... -count=1 -race -v
```

### Phase 5 完成后全量验证
```bash
go build ./...
go vet ./...
gofmt -l .  # 仅 pkg/compliance/appropriateness.go 预存在 issue（非本任务范围）
go test ./... -count=1 -race -short  # 排除 e2e
```

### 端到端验证（Phase 4 测试覆盖）
- 构造一个含 `expression:` section 的 YAML 字符串
- 调用 `pipeline.ExecuteFromYAML(ctx, yamlStr, mockRunner)`
- 验证：strategy 被注册、mock runner 被调用、Result.BacktestResult 非空
- 验证 round-trip：`intent` (含 `signal_expr` 参数) → `Generate` → `LoadStrategy` → `Parameters()` 与原 intent 一致

### 测试质量检查（AGENTS.md §8.3 规范 1）
- 所有测试有行为断言（非 placeholder）
- 覆盖边界：空输入、非法输入、默认值、注册冲突
- 测试名描述被测行为（如 `TestLoadStrategy_InvalidDirection`）
- 无 `time.Sleep`（本任务无并发，N/A）

## 风险与缓解

| 风险 | 缓解 |
|------|------|
| yaml.v3 round-trip 字符串引号问题 | Phase 3 测试 14 专门覆盖特殊字符；输出时字符串字段用双引号 |
| `expression_template` 自注册冲突 | LoadStrategy 显式拒绝该名字 |
| 注册同名策略冲突 | ExecuteFromYAML 检测 + Configure 覆盖 |
| 顶层 `risk:` vs `expression.risk:` 语义混淆 | SPEC.md 用表格明确两者用途 |
| yaml.v3 依赖提升触发 "Ask First" | 已是 indirect dep，仅提升为 direct，非新增依赖；commit message 注明 |
