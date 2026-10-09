# quant-trading-agent

Quant Lab 的 **AI 实验员层**。从 monorepo `quant-trading` 拆出（2026-10-09）。

## 定位与边界

本仓只含 **AI 实验员**领域。后端内核（回测引擎、策略运行时、表达式 DSL、数据供给）在
[`quant-trading`](https://github.com/ruoxizhnya/quant-trading)，前端在
[`quant-trading-ui`](https://github.com/ruoxizhnya/quant-trading-ui)，跨领域文档在
[`quant-trading-docs`](https://github.com/ruoxizhnya/quant-trading-docs)。

切分判据是 **DDD**：「怎么算」= core（仪器）；「试什么 / 怎么试 / 试完怎么判」= AI（实验员）。

## 目录

| 路径 | 内容 |
|---|---|
| `docs/hermes/` | Hermes agent 的配置 / 提示词 / 技能 / 系统设计 / 工具契约（原 monorepo 的 `docs/hermes/`） |

## ⚠️ 当前状态：文档先行，代码未迁

2026-10-09 只迁出了 AI 领域的**文档**。`pkg/ai/` 的 Go 代码仍在 `quant-trading`（core）仓，
**尚未迁出**——原因见下。

## 为什么 AI 代码还没迁（重要）

AI 代码拆仓有硬前提：`quant-trading` 的 `cmd/analysis`（组合根）import 了 `pkg/ai`
（13 个文件），而 `pkg/ai` 又依赖 core 的 21 个包 ⇒ 若现在直接拆，AI 仓 require core 仓、
core 仓的 cmd require AI 仓，构成**仓级环**，Go module 不允许。

拆仓前置 = **ADR-027 §5 第 8 步（ai-service 服务化）**：把 `cmd/analysis` 里的 AI handler
（explore/pipeline/tools）搬进 ai-service，core 改用 HTTP 调 AI，环才消失。

在此之前，AI 代码在 core 仓内用 `go.work` 双 module 做编译期边界固化。
