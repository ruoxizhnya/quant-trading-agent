---
status: active
last-verified: 2026-09-18
verified-by: 从 pkg/tools/builtin/ 的实现与注释反推（P2-11）
---

# Hermes Agent 集成 — 系统设计

> ⚠️ **本文档是反推补齐的，不是原始文档。**
>
> 原始 `hermes-agent-integration-system-design.md` 位于 `.trae/documents/`
> （Trae IDE 会话级目录），已随目录删除而永久遗失 —— 2026-09-16 CI 文档
> 校验时发现代码里有 4 处引用指向一个不存在的文件（TASKS P2-11）。
>
> 本文档的内容来源是**代码本身**：`pkg/tools/builtin/` 各工具的实现与
> 注释里逐条记录了「设计文档说 X，我们做了 Y，原因 Z」，把这些偏离记录
> 提炼出来就是现在的 §3.2 与 §6.2。**只有这两节是复原的**，其余章节
> 当前代码没有引用，不凭空补写 —— 需要时按同样的办法从代码反推。

## 与 ODR-046 的分工

| 文档 | 回答什么 |
|---|---|
| [ODR-046](../archive/odr/odr-046-hermes-agent-integration-decision.md) | **决策**：选 Hermes Agent 而不是自研前端 AI 组件，为什么 |
| 本文档 | **设计**：工具契约是什么，实现相对设计偏离了什么 |

决策记录在 ODR，设计细节在这里。两者冲突时以 ODR 为准（它是决策，本文是描述）。

## §3.2 工具参数契约

工具层是 Hermes 唯一能操作底座的通道（19 → 20 个工具，见
`docs/hermes/tools-quant-backtest.yaml`）。每个工具的输入参数在设计文档
里都有约定，实现相对约定有 **3 处偏离**，全部记录在下表，并在对应源码
注释里留了 `Design deviation` 标记。

### 偏离 1：`factor.evaluate` — `universe` vs `symbols`

- 设计：参数叫 `universe`（默认 `"csi300"`），由 universe resolver 把
  命名池映射成具体标的列表。
- 实现：直接接受 `symbols`（`[]string`）。
- 原因：universe resolver 需要 storage 或 data-service 支撑，属 Phase 2
  工作。Phase 1 让调用方直接给标的列表，避免为一个尚不存在的解析层
  先造一个假的。
- 源码：`pkg/tools/builtin/factor_tools.go`

### 偏离 2：`list_strategies` — `min_sharpe` vs `min_fitness`

- 设计：过滤参数叫 `min_sharpe`。
- 实现：改叫 `min_fitness`。
- 原因：`StrategyPool.List` 实际过滤的是 `minFitness`（复合适应度评分），
  不是 Sharpe。沿用设计文档的名字会让调用方以为自己在按 Sharpe 过滤，
  而实际行为是另一回事 —— 名字必须反映真实行为。
- 源码：`pkg/tools/builtin/gene_pool_tools.go`

### 偏离 3：`walk_forward_validate` — `strategy_yaml` vs `strategy_name`

- 设计：传 `strategy_yaml`，让 Hermes 能校验一个临时的、未注册的 YAML
  策略。
- 实现：传 `strategy_name`（已注册的策略）。
- 原因：前者需要「从 YAML 注册临时策略」的能力，属 Phase 2。
- 源码：`pkg/tools/builtin/walkforward_tool.go`

## §6.2 GateDecision — 门控元数据

L1–L4 每层验证工具（factor / backtest / walk-forward）的返回值里都内嵌
一个 `GateDecision`，让 Hermes 用**同一套字段**判断「这道门过了没有」，
而不必逐个工具去解析各自的返回结构。

```
GateDecision {
    Level           string  // "L1" | "L2" | "L3" | "L4"
    Passed          bool
    Reason          string  // "passed" | "low_ic" | ...
    Recommendation  string  // 面向 LLM 的可操作提示（中文）
}
```

- `Reason` 是**机器可读**的枚举式短码，不是给人读的句子。
- `Recommendation` 反过来是给 LLM 看的下一步建议。两者分开，是为了让
  「判断」和「行动建议」各归其位 —— 混在一起会让 agent 把提示当结论。
- 源码：`pkg/tools/builtin/gate.go`

## 相关

- Skill 定义：[skills/autonomous_factor_mining.md](skills/autonomous_factor_mining.md)
- Agent 配置（模型 / 记忆 / 预算）：[config/hermes.yaml](config/hermes.yaml)
- 工具清单：[tools-quant-backtest.yaml](tools-quant-backtest.yaml)
- 验收测试：[e2e-acceptance-test.md](e2e-acceptance-test.md)（Phase 2.6）
- 研究员 prompt：[prompts/quant-research.md](prompts/quant-research.md)
