# quant-trading-agent

Quant Lab 的 **AI 实验员层**。从 monorepo `quant-trading` 拆出（2026-10-09）。

## 定位与边界

本仓含 **AI 实验员**领域的全部代码与文档。后端内核（回测引擎、策略运行时、表达式 DSL、
数据供给）在 [`quant-trading`](https://github.com/ruoxizhnya/quant-trading)（core，
本仓**单向依赖**它），前端在 [`quant-trading-ui`](https://github.com/ruoxizhnya/quant-trading-ui)，
跨领域文档在 [`quant-trading-docs`](https://github.com/ruoxizhnya/quant-trading-docs)。

切分判据是 **DDD**：「怎么算」= core（仪器）；「试什么 / 怎么试 / 试完怎么判」= AI（实验员）。

## 目录

| 路径 | 内容 |
|---|---|
| `pkg/ai/` | AI 编排层（loop / pipeline / gene_pool / tools / agents / causal / …，24 包）——**自 core 仓迁入（2026-10-09，AI 拆仓阶段 2）** |
| `pkg/copilot/` | `CopilotService`（自然语言 → 生成代码 → 沙箱编译 → 回测）——自 core 的 `pkg/strategy/copilot.go` 归位 |
| `cmd/ai/` | **ai-service**（:8086）：Copilot / pipeline / explore / tools 四族 HTTP 面 + AI 侧装配 builder |
| `docs/hermes/` | Hermes agent 的配置 / 提示词 / 技能 / 系统设计 / 工具契约 |

## 依赖方向（结构性不变量）

```
agent ──require──► core（github.com/ruoxizhnya/quant-trading，本地 replace ../quant-trading）
core ────────────► ✗ 永不反向依赖 agent
```

core 仓的 `internal/repoguard` 有护栏钉住这个方向：core 里**不许出现 `pkg/ai` 目录**、
生产代码不许 import `pkg/ai` 或 agent 模块。反向边 = 重新制造仓级环 = Go module 直接拒绝编译。
（这条不变量正是本仓曾长期「代码未迁」的原因——前置是 ADR-027 §5 第 8 步的 ai-service
服务化，2026-10-09 落地：四族 handler 先进独立进程、analysis 改反代，环从进程边界消失，
随后才可能做零反向边的物理搬迁。）

## 构建与测试

```bash
# 前提：core 仓 checkout 在同级目录（../quant-trading）
go build ./... && go vet ./... && go test ./... -count=1
```

CI（`.github/workflows/ci.yml`）把 core checkout 到 `core/` 后用
`go mod edit -replace` 改写路径，同样的单向依赖在远端成立。

## ai-service 的镜像构建

go.mod 的 replace 是相对路径，Docker 构建靠**命名构建上下文**提供 core 源码：

```bash
docker build -t ai-service --build-context core=../quant-trading .
```

core 仓的 `docker-compose.yml` 已配置 `additional_contexts: core=.`（同效）。
ai-service 在 compose 里发布 `127.0.0.1:8086`，analysis-service 对
`/api/{copilot,pipeline,explore,tools}` 做纯反代 —— 前端与 openapi 契约不感知拆仓。
