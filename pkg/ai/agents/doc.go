// Package agents implements the Go-native AI research agents (Research,
// Generate, Validate, Evolve) for the Phase 4 quantitative research pipeline.
//
// # 废弃状态 —— 2026-09-18 更正（TASKS P2-5）
//
// 这里原先写着 "should NOT be used in new code"。**这句话是错的**，已经
// 造成误导：`pkg/ai/pipeline`（P1-1b 的探索主链路）就在用 ResearchAgent /
// GenerateAgent / ValidateAgent，那不是遗留代码，是当前主链路的一部分。
//
// 准确的说法是**分层的**，ODR-046 废的是交互层不是能力层：
//
//   - **交互层（已废）**：原计划的「前端 AI 组件」从未建成，由 Hermes
//     Agent 的自然语言交互取代。这才是 ODR-046 真正废掉的东西。
//   - **能力层（在用）**：本包的 Go agent 是 pipeline 的实现细节。Hermes
//     通过 MCP 工具层（pkg/ai/tools/builtin/）调用底座，`cmd/analysis` 的
//     pipeline 则在进程内直接调用本包 —— 两条路都通到同一批能力。
//
// 所以：新代码**不直接面向用户**暴露这些 agent，但在 pipeline 内部继续
// 使用它们是正常的。曾经唯一直接面向外部的是 `cmd/ai`（两条 HTTP 路由），
// 它零调用方且建在废弃交互层的定位上，已于 2026-09-18 删除（P2-5）。
//
// 新研究流程优先走 MCP 工具层 + Hermes Skill：
// docs/hermes/skills/autonomous_factor_mining.md。
//
// References:
//   - docs/archive/odr/odr-045-frontend-ai-component-deprecation.md
//   - docs/archive/odr/odr-046-hermes-agent-integration-decision.md
//   - docs/hermes/skills/autonomous_factor_mining.md
//   - docs/hermes/config/hermes.yaml
package agents
