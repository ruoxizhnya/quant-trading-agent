# TASKS — quant-trading-agent 未完成项

> 本仓（AI 实验员层）的任务台账。**六字段规范同 core 仓 `AGENTS.md` §8.5**（quant-trading 仓）：目标 / 上下文 / 要求 / 约束与非目标 / 验收 / 边界，且须满足「独立 agent 拿着本单 + 它显式引用的冻结契约，不需读其他实现、不需问人，就能完成并自证」。
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞` / `✅ 完成（附日期）`。
> 本文件自 core 台账 OPS-01 改判（2026-10-14）派生——原单前提（core 侧做 explore SSE）因 AI 拆仓失效，实现落此仓。

---

## OPS-01-A · 观察页 run 状态 SSE 推送端点 ✅ 完成（2026-10-15）

> **收口记录（2026-10-15，实现 + 自证）**：新增 `cmd/ai/handlers_explore.go` 的 `Stream`（路由 `GET /explore/runs/:runID/stream`）+ 抽出可测的 `pumpStream(ctx, interval, snapshot, emit)`；把 `Status` 与 `Stream` 共用 `statusOf` 保证两条路 JSON 一字不差。硬约束守住：断线只停推、`Stream`/`pumpStream` 结构上拿不到 run 的 cancel，绝不取消探索；未动 core 限流器；轮询端点 `GET /runs/:runID` 行为不变。**验证**（本机无 Go SDK，在 `golang:1.25-alpine` 容器内跑，离线用宿主 module 缓存）：`gofmt -l` 空、`go build ./...` 过、`go vet ./cmd/ai/...` 过、`go test ./cmd/ai/... -run 'Explore|PumpStream' -race -count=1` **绿**（含全部既有 explore 测试回归）；新增测试 `handlers_explore_stream_test.go`：3 个 pumpStream 逻辑测（完成→progress+complete、running 每 tick progress、ctx 取消停在 progress 不发 complete）+ 2 个 HTTP 测（未知 run 404、已完成 run 真实 SSE 帧）。**破坏验证**：把 `emit("progress"` 改 `emit("process"` → HTTP 测当场红（帧变 `event:process` 不含 `event:progress`）→ 还原绿。注：gin v1.10 SSE 帧冒号后无空格（`event:progress`）。


- **① 目标**：ai-service 暴露 `GET /api/explore/runs/:runID/stream`（SSE），把一轮探索的实时状态推给客户端，使观察页从「每 2.5s 轮询 `GET /runs/:runID`」变成「开一条长连接」，消除 429 与真失败混淆的土壤。**core 侧无需改动**（ai-proxy 已 `FlushInterval=-1` 做 SSE 逐写直通）。
- **② 上下文**（本仓代码锚点，开工前逐一读）：
  - 现轮询端点 `Status`：`cmd/ai/handlers_explore.go:340`（路由 `g.GET("/runs/:runID", h.Status)`，注册于 `:164`）。它从 `run.mu` 保护的快照拼 `ExploreStatusResponse{RunID, Running: !run.done, Stopped, Attempts, BestSeq}`（类型定义 `:142`）。
  - 运行态来源：`Start`（`:203`）起一个**独立于请求生命周期**的 goroutine（`:220-222` 注释：用 `context.Background()` 派生 cancel，绝不能用 `c.Request.Context()`），异步经 `Config.OnAttempt`（`:250`）把每次尝试 append 进 `run.attempts`，跑完在 `defer` 里置 `run.done = true`（`:242`）。
  - **无 pub/sub**：`run`（`exploreRun` 结构 `:105`）只是被 poll 的内存快照——与 core 先例 data-service `sseProgressHandler`（core 仓 `cmd/data/sync_handlers.go:761`，每 2s 轮询 store 再转推 SSE）**同构**。本单复用该「轮询快照→SSE」形状，不另造订阅机制。
  - 冻结契约（照此对齐，勿改）：`loop.Attempt`（`pkg/ai/loop/loop.go:71`）、`loop.RunResult` / `RunResult.Stopped StopReason`（`pkg/ai/loop/loop.go:89`、`:94`）、`ExploreStatusResponse`（`handlers_explore.go:142`）。
  - 裁决/因果维落库在 `OnAttempt` / `reviewBestCausally` 里**持 `run.mu` 锁**读写同一 run（`:251`、`:331`）——SSE 侧读快照也必须持同一把锁，不得绕过。
  - 前端自救（`POLL_MS=2500` + 连续 6 次失败才认断线 + e2e 钉住）在 **ui 仓**，接线另立小单（跨仓两段式）。core 台账登记见 core `docs/TASKS.md` OPS-01 改判记录。
- **③ 要求**：
  1. `RegisterExploreRoutes`（`handlers_explore.go:160`）内新增 `g.GET("/runs/:runID/stream", h.Stream)`。
  2. **开流前先校验 run 存在**：不存在 → `httpserver.Fail(c, 404, "run not found")` 直接返回（沿用 `Status` 的 not-found 语义），不给一个「一连上就 error」的 EventSource。
  3. SSE 头与事件形状对齐 data-service 先例：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`X-Accel-Buffering: no`；用 gin `c.SSEvent(name, data)` 且每次 `c.Writer.Flush()`。事件名：`progress`（payload = 当下 `ExploreStatusResponse` 快照，**与轮询端点同 JSON 形状**，便于 ui 复用类型）、终止时 `complete`（最终快照）、客户端断线 `close`、取快照出错 `error`。
  4. 主循环（ticker，间隔复用先例语义即可，如 2s）：`select` 两路——`<-ctx.Done()`（`c.Request.Context()`，仅控**推送**）→ 发 `close` 返回；`<-tick` → 持 `run.mu` 取快照发 `progress`，若 `run.done` 则发 `complete` 后返回。
  5. **⚠️ 断线不叫停探索**：`Stream` 用的请求 ctx 取消只是停推，绝不能触碰 `run.cancel` / `run.ctrl`（叫停只走既有 `POST /runs/:runID/stop`）。
  6. **只增不改**：保留 `GET /runs/:runID` 轮询端点（其他调用方 + ui 回退路），`Status`/`Start`/`Stop` 的签名与 JSON 契约不变。
- **④ 约束与非目标**：不改 `Status`/`Start`/`Stop` 既有行为；**不动 core 限流器**（§八 逐字匹配裁决继续有效——本单靠「N 请求→1 长连接」省 429，不靠豁免路径）；不动 loop / pipeline 编排逻辑；不做前端接线（ui 另立）；不新增第三方依赖；**不引入多订阅 fan-out**（单观察者轮询快照足够，过度设计留给真有需求时）。
- **⑤ 验收**：
  1. 新增单测（`package main`，gin + `httptest` + testify，**禁 `time.Sleep` 同步**，用可控 fake / `require.Eventually`）：注入一条可控的 run（fake `loop.TryRunner` 或直接摆 `run.attempts`/`done`），断言 `GET /runs/:id/stream` 按序发出 ≥1 个 `event: progress`、终止时发 `event: complete` 后连接结束；`run` 不存在时返回 404（且未开流）。
  2. **破坏验证**：① 把终止判定取反 / 让 `done` 永不为真 → 断言 `complete` 的用例变红；② 改事件名（如 `progress`→`process`）→ 断言事件名的用例变红；③ 让 Stream 误用 `run.cancel`（断线即叫停探索）→ 断言「探索仍在跑」的用例变红；均还原绿。
  3. 回归：现有 `Status` 相关测试不退化；`go build ./...`、`go vet ./...`、`go test ./cmd/ai/... ./pkg/ai/loop/... -race` 全绿。
- **⑥ 边界**：只加 explore run 状态的 SSE 通道；copilot job / pipeline 等其他轮询端点另立。
