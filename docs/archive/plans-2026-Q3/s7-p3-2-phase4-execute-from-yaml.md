# S7-P3-2 Phase 4–5: Pipeline.ExecuteFromYAML + 完成 S7-P3-2

> **分支**: `feat/s7-p3-2-yaml-expression-loader`
> **前置状态**: Phase 1–3 已完成（commits `82bc081`, `0d46227`, `9c1d556`）
> **本计划范围**: Phase 4（ExecuteFromYAML + 测试 + 文档）+ Phase 5（标记完成）

---

## Summary

完成 S7-P3-2 的最后一步：在 `pkg/ai/pipeline/pipeline.go` 增加 `ExecuteFromYAML` 方法，让 AI 生成的 YAML 可直接执行回测，**完全绕过 LLM Go-codegen + `go build` 编译路径**。这闭合了 ADR-015 §3 "AI as quant researcher" 的循环：自然语言 → 意图 → YAML → ExpressionStrategy → 回测。

---

## Current State Analysis

### 已完成（Phase 1–3, commits 82bc081 / 0d46227 / 9c1d556）

1. **YAML schema** — `Config.Expression ExpressionYAML` 字段 + `SignalYAML`/`SizingYAML`/`RiskYAML` 类型
2. **`ParseConfig`** — 用 `yaml.v3` 解析 YAML → Config（5 个测试）
3. **`LoadStrategy`** — 解析 + 构建 `*expression.ExpressionStrategy`，含检测逻辑（expression section vs `type: expression`）+ 方向校验 + 保留名拒绝（11 个测试）
4. **`LoadAndRegister`** — LoadStrategy + GlobalRegister 便捷封装
5. **Generator 扩展** — `intentToExpressionConfig` 从 intent 提取 expression 参数，`configToYAML` 输出 `expression:` section（3 个测试，含 round-trip + 特殊字符引用）

### 缺失（Phase 4 需实现）

- `Pipeline.ExecuteFromYAML(ctx, yamlStr, runner)` 方法 — 当前 pipeline 只有 `Execute`（依赖 LLM codegen）和 `ExecuteAsync`
- 注册冲突处理 — 同名策略重复执行时的 Configure-in-place 路径
- 文档同步 — SPEC.md / ARCHITECTURE.md 需补充 YAML 直执行路径

---

## Proposed Changes

### 文件 1: `pkg/ai/yaml/loader.go`（修改）

**新增导出函数 `ExpressionParamsFromConfig`**

```go
// ExpressionParamsFromConfig builds the flat params map that
// ExpressionStrategy.Configure expects, from a parsed Config's
// Expression section. Only non-zero values are included so partial
// updates preserve existing config (Configure semantics).
//
// Used by Pipeline.ExecuteFromYAML to reconfigure an already-registered
// strategy in place when the same name is loaded again.
func ExpressionParamsFromConfig(config *Config) map[string]interface{} {
    expr := config.Expression
    params := map[string]interface{}{
        "signal_expr": expr.Signal.Expression,
    }
    if expr.Signal.Action != "" {
        params["action"] = expr.Signal.Action
    }
    if expr.Signal.Direction != "" {
        params["direction"] = expr.Signal.Direction
    }
    if expr.Signal.MinStrength != 0 {
        params["min_strength"] = expr.Signal.MinStrength
    }
    if expr.Signal.Lookback != 0 {
        params["lookback"] = expr.Signal.Lookback
    }
    if expr.Sizing.Method != "" {
        params["sizing_method"] = expr.Sizing.Method
    }
    if expr.Sizing.FixedWeight != 0 {
        params["fixed_weight"] = expr.Sizing.FixedWeight
    }
    if expr.Sizing.MaxPerStock != 0 {
        params["max_per_stock"] = expr.Sizing.MaxPerStock
    }
    if expr.Sizing.MaxTotal != 0 {
        params["max_total"] = expr.Sizing.MaxTotal
    }
    if expr.Risk.MaxPositionPct != 0 {
        params["max_position_pct"] = expr.Risk.MaxPositionPct
    }
    if expr.Risk.MaxOpenPositions != 0 {
        params["max_open_positions"] = expr.Risk.MaxOpenPositions
    }
    if expr.Risk.MinCashBuffer != 0 {
        params["min_cash_buffer"] = expr.Risk.MinCashBuffer
    }
    return params
}
```

**Why**: 把 param-name 字符串集中在此包（与 `intentToExpressionConfig` 对称：intent→YAML 和 YAML→params），避免 pipeline 重复硬编码 param 名。当 `Expression` section 为空（`type: expression` 路径），返回的 map 只有 `signal_expr: ""`，Configure 会保留现有值。

### 文件 2: `pkg/ai/yaml/loader_test.go`（修改，`git add -f`）

**新增 1 个测试**

- `TestExpressionParamsFromConfig` — 验证全字段填充 + 零值省略 + 空 expression section

### 文件 3: `pkg/ai/pipeline/pipeline.go`（修改）

**新增 import**: `pkg/strategy/expression`

**新增方法 `ExecuteFromYAML`**

```go
// ExecuteFromYAML runs a backtest directly from a YAML strategy config,
// bypassing the LLM code-generation + compile path. The YAML must
// describe an expression-type strategy (either via an `expression:`
// section or `strategy.type: expression`).
//
// Flow:
//  1. Parse YAML → Config (for universe/dates) + LoadStrategy → Strategy
//  2. Registration: GlobalGet(name) → if same type, Configure in place;
//     if different type, error; if not found, GlobalRegister
//  3. runner.RunBacktest(ctx, name, universe, startDate, endDate)
//
// Result.YAMLConfig is populated; GeneratedCode/BuildError are left
// empty (no codegen occurred). If runner is nil, backtest is skipped
// and the result is marked complete after registration.
func (p *Pipeline) ExecuteFromYAML(ctx context.Context, yamlStr string, runner BacktestRunner) (*Result, error) {
    result := p.StartJob("yaml-direct-execution")

    // Stage 1: Parse YAML + build strategy
    p.log(result, "Stage 1/3: Parsing YAML and loading strategy...")
    config, err := yamlgen.ParseConfig(yamlStr)
    if err != nil {
        p.fail(result, StageParse, fmt.Sprintf("YAML parse failed: %v", err))
        return result, err
    }
    result.YAMLConfig = yamlStr

    s, err := yamlgen.LoadStrategy(yamlStr)
    if err != nil {
        p.fail(result, StageGenerate, fmt.Sprintf("Strategy load failed: %v", err))
        return result, err
    }

    // Stage 2: Register or reconfigure
    p.log(result, "Stage 2/3: Registering strategy...")
    if err := p.registerOrConfigure(s, config); err != nil {
        p.fail(result, StageGenerate, fmt.Sprintf("Registration failed: %v", err))
        return result, err
    }

    // Stage 3: Backtest
    if runner != nil {
        p.log(result, "Stage 3/3: Running backtest...")
        universe := parseUniverse(config.Data.Universe)
        startDate := config.Backtest.StartDate
        endDate := config.Backtest.EndDate
        if startDate == "" {
            startDate = "2022-01-01"
        }
        if endDate == "" {
            endDate = "2024-01-01"
        }
        btResult, err := runner.RunBacktest(ctx, s.Name(), universe, startDate, endDate)
        if err != nil {
            p.fail(result, StageBacktest, fmt.Sprintf("Backtest failed: %v", err))
            return result, err
        }
        result.BacktestResult = btResult
        p.log(result, "Backtest completed successfully")
    } else {
        p.log(result, "Stage 3/3: Skipping backtest (no runner provided)")
    }

    p.complete(result)
    return result, nil
}
```

**新增私有方法 `registerOrConfigure`**

```go
// registerOrConfigure handles strategy registration with collision
// resolution. If name is not registered, it registers s. If a strategy
// with the same name exists and is also an *expression.ExpressionStrategy,
// it reconfigures the existing one in place (so live backtest engines
// holding the existing reference see the update). If the existing
// strategy is a different type, it returns an error.
func (p *Pipeline) registerOrConfigure(s strategy.Strategy, config *yamlgen.Config) error {
    name := s.Name()
    existing, err := strategy.GlobalGet(name)
    if err != nil {
        // Not registered — register new.
        return strategy.GlobalRegister(s)
    }
    // Collision — require same concrete type.
    existingExpr, ok1 := existing.(*expression.ExpressionStrategy)
    _, ok2 := s.(*expression.ExpressionStrategy)
    if !ok1 || !ok2 {
        return fmt.Errorf(
            "pipeline: strategy name %q already registered with a different (non-expression) type",
            name)
    }
    // Same type — reconfigure existing in place.
    params := yamlgen.ExpressionParamsFromConfig(config)
    c := strategy.AsConfigurable(existingExpr)
    if c == nil {
        return fmt.Errorf("pipeline: existing strategy %q is not Configurable", name)
    }
    return c.Configure(params)
}
```

**Why type assertion on `*expression.ExpressionStrategy`**: LoadStrategy 只会返回这个具体类型。若已注册同名策略是 MomentumStrategy 等，对它调用 `Configure(signal_expr=...)` 会静默忽略未知参数 → 隐藏 bug。类型断言让冲突显式失败。Pipeline → expression 的依赖方向合法（pipeline 已依赖 strategy + yaml，yaml 已依赖 expression）。

### 文件 4: `pkg/ai/pipeline/pipeline_test.go`（修改，`git add -f`）

**新增 4 个测试**（用 `time.Now().UnixNano()` 后缀保证 `-count=2` 不冲突）

1. **`TestPipeline_ExecuteFromYAML_HappyPath`** — 完整 YAML → 策略加载 + 注册 + mock runner 返回结果 → 验证 `Result.YAMLConfig`/`BacktestResult`/`Status==StageComplete`/`GeneratedCode==""`

2. **`TestPipeline_ExecuteFromYAML_RegistrationCollision`** — 预先用 `GlobalRegister` 注册一个 expression strategy（同名），再调 `ExecuteFromYAML` → 应 Configure 而非报错，验证 `Status==StageComplete`

3. **`TestPipeline_ExecuteFromYAML_InvalidYAML`** — 畸形 YAML → `StageFailed` + error 含 "YAML parse failed"

4. **`TestPipeline_ExecuteFromYAML_NilRunner`** — 合法 YAML + nil runner → 跳过回测，仍 `StageComplete`，`BacktestResult==nil`

**测试隔离策略**: 所有预注册的策略名用 `fmt.Sprintf("test_yaml_%s_%d", t.Name(), time.Now().UnixNano())` 保证跨测试、跨 `-count=2` 唯一。HappyPath/NilRunner 不预注册，用唯一名即可直接 GlobalRegister（首次成功）。

### 文件 5: `docs/SPEC.md`（修改）

在 "AI Pipeline Integration (S7-P3-2)" 章节末尾追加 "#### Direct Execution via ExecuteFromYAML" 子节：

- 流程图：`YAML → ParseConfig + LoadStrategy → registerOrConfigure → RunBacktest`
- API 表：`Pipeline.ExecuteFromYAML(ctx, yamlStr, runner) (*Result, error)`
- 行为说明：绕过 codegen/compile；冲突时同类型 Configure、异类型 error；universe/dates 取自 YAML `data.universe`/`backtest.start_date`/`backtest.end_date`（默认 2022-01-01 → 2024-01-01）
- `Result` 字段差异：`YAMLConfig` 填充，`GeneratedCode`/`BuildError` 留空

### 文件 6: `docs/ARCHITECTURE.md`（修改）

在 "### 策略生成流水线 (Pipeline)" 小节（约 903 行）的流程图后追加一段：

> **YAML 直执行路径 (S7-P3-2)**: 当策略为 expression 类型时，`Pipeline.ExecuteFromYAML` 绕过 LLM codegen + 编译，直接 `LoadStrategy → registerOrConfigure → RunBacktest`。适用于 AI 迭代调参场景（同 YAML 多次执行 / 微调 expression 后重跑）。

### 文件 7: `docs/TASKS.md`（修改）

将 S7-P3-2 行的状态从 `⬜` 改为 `✅`。

---

## Assumptions & Decisions

| # | 决策 | 理由 |
|---|------|------|
| D1 | `ExecuteFromYAML` 是同步方法（非 async） | 与 `Execute` 对称；async 版本可后续按需加 `ExecuteFromYAMLAsync` |
| D2 | 冲突处理用类型断言 + Configure-in-place | 避免 scope creep（不加 Registry.Replace/Unregister）；同类型可安全重配；异类型显式报错 |
| D3 | universe/dates 从 YAML `data`/`backtest` section 提取 | 与 `runBacktest` 的 `parseUniverse(intent.Universe)` 对称；缺失时 fallback 到 2022-01-01→2024-01-01（与现有 `runBacktest` 一致） |
| D4 | `ExpressionParamsFromConfig` 放在 yaml 包 | param 名与 `intentToExpressionConfig` 对称集中；pipeline 不重复硬编码 param 名字符串 |
| D5 | 测试用 `UnixNano` 后缀名 | 全局 registry 是包级状态，固定名会在 `-count=2` 时冲突；`Configure-in-place` 让 HappyPath 重跑安全，但 TypeCollision 预注册需唯一名 |
| D6 | 不新增 `Registry.Unregister` | 超出 S7-P3-2 范围；若后续需要可独立任务 |

---

## Verification Steps

### Phase 4 验证

```bash
# 1. 构建
go build ./...

# 2. 静态检查
go vet ./...
gofmt -l pkg/ai/yaml/loader.go pkg/ai/pipeline/pipeline.go

# 3. 单元测试（含新测试）
go test ./pkg/ai/yaml/... -v -count=1 -race
go test ./pkg/ai/pipeline/... -v -count=1 -race

# 4. 全套件 flakiness gate（per AGENTS.md §8.3 规范 1）
go test ./... -race -count=2
```

### Phase 5 验证

- `docs/TASKS.md` 中 S7-P3-2 行为 `✅`
- `docs/SPEC.md` / `docs/ARCHITECTURE.md` 交叉引用一致

### 提交计划

| Commit | Type | 内容 |
|--------|------|------|
| 1 | `feat(ai/pipeline)` | `add ExecuteFromYAML bypassing codegen` — 含 loader.go helper + pipeline.go 方法 + 测试 + SPEC/ARCHITECTURE 文档 |
| 2 | `chore(docs)` | `mark S7-P3-2 complete` — TASKS.md 状态更新 |

两个 commit 都在 `feat/s7-p3-2-yaml-expression-loader` 分支上，最后整体 merge to main。

---

## 提交前检查清单

- [ ] `go build ./...` 通过
- [ ] `go vet ./...` 无警告
- [ ] `gofmt -l .` 无输出
- [ ] `go test ./... -race -count=2` 通过（excl. /e2e）
- [ ] 新测试文件 `git add -f`（`*_test.go` 被 .gitignore）
- [ ] binary artifacts（`analysis`/`data`）未 staged（`git restore --staged` 若误入）
- [ ] commit message 含 `Refs: S7-P3-2` + `Reviewed: ...`
