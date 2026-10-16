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

---

## OBS-01-R1-A · 偏差维输出一个它没量过的数字 ✅ 完成（2026-10-14；2026-10-16 自 core 台账 OBS-01-R1 迁入）

> **迁入说明（2026-10-16）**：本单实现整体在本仓（`pkg/ai/validation/bias.go` + `cmd/ai/handlers_explore.go`），core 侧原条目已移出、只留索引指针（core `docs/TASKS.md` §三）。以下六字段与完成记录逐字保留自 core 原条目，不改写。

- **① 目标**：验证器偏差维在池子规模未测量时**不打印数量**（「0 只」不再冒充实测值），屏幕上每个数字都可被当场质问。
- **② 上下文**：归档卷 §八工单表 #7（2026-10-14 立单，**代码在 quant-trading-agent 仓**）：`cmd/ai/handlers_explore.go:50` 设 `PoolSource=PoolSourceCurrent` 但从不设 `PoolSize`（只有 `:96` 预热成功路径才填）⇒ `pkg/ai/validation/bias.go:296-303` 无条件把 `in.PoolSize`（Go 零值 0）插进 blocking 文案 ⇒「股票池按今天的上市名单取（**0 只**）」。**0 是零值不是测量结果**（愿景原则 4：每个数字可被当场质问）。`PoolSize<=0` 的「未测量」分支已有先例（`bias.go:202` 与 `:314`），只有 `PoolSourceCurrent` 这支漏了。**概率侧没有 bug**：`biasProbability` 对 `PoolSourceCurrent` 提前返回 `BiasProbFloor`，低分有意为之，错的只有那句话。
- **③ 要求**：`PoolSize<=0` 时不带数量措辞（对齐 `bias.go:202`/`:314` 既有「未测量」分支先例）；文案改动最小化。
- **④ 约束与非目标**：**不改 `biasProbability` 与 `PoolSourceCurrent` 的概率**；不借机重构偏差维；不动其他维。
- **⑤ 验收**：单测：`PoolSize=0`（未测量）输出无数量词、`PoolSize>0` 输出带真实数量；**破坏验证**：改回无条件打印 → 新测试红，还原绿。正常 run 输出文案回归不变（有测量值场景）。
- **⑥ 边界**：只改这一处措辞分支。
- **✅ 完成记录（2026-10-14）**：**代码修复由并发 agent 落地**（quant-trading-agent 仓 `f432074`）：`biasChallenges` 幸存者 switch 新增 `PoolSource == PoolSourceCurrent && PoolSize <= 0` 前置分支，文案改「（池子规模未测量）」，对齐 `bias.go:202`（概率侧未评估返回 0）与 `bias.go:323-328`（文案侧「幸存者偏差未评估」note）既有先例（任务原文引 `:314` 系笔误，该行实为「池子不小却无退市股」的带数量 warning 分支）；severity 保持 blocking，`biasSurvivorship` 对 `PoolSourceCurrent` 仍返回 `BiasProbFloor`——④「概率不动」达成，③「文案改动最小化」达成（+9 行）。**两腿单测**：`TestBias_CurrentPoolWithoutSizeSaysUnmeasured`（零值输出含「未测量」且不含「0 只」）+ `TestBias_CurrentPoolWithSizePrintsCount`（PoolSize=300 带真实数量，⑤ 回归场景）。**本单收口复验（独立重做，不轻信原提交声称）**：`go test ./pkg/ai/validation/ -count=1` 全绿（42.4s）、gofmt/vet 干净；**破坏验证**：把该分支改回原始 bug 形态（无条件 `fmt.Sprintf` 打印零值）→ `TestBias_CurrentPoolWithoutSizeSaysUnmeasured` 精确变红 → 还原 → 复绿，`git diff` 零残留；另做过一次「只拆条件不改文案」的变体验证（红在 WithSize 腿，亦还原），两变体共同证明护栏非空转。**接线现状核对**（`cmd/ai/handlers_explore.go`）：静态 `exploreBias`（:40-53）不带 `PoolSize` = 未测量路径，文案不再冒充实测值；`biasInput()`（:86-103）在引擎预热到上市日历时填 `PoolSize=len(windows)` = 测量路径带真实数量。**提交方式说明**：本标记在 D3 批次（core `5225df5`）提交后以 pathspec 单文件提交，未触碰并发 agent 在途的 7 个无关脏文件。

---

## OBS-01-R2-A · AI 客户端把 K9 三值丢在边界上 ⬜ 待做（2026-10-16 自 core 台账 OBS-01-R2 迁入，裁决 (b)）

> **来源与裁决**：本单自 core 仓 `docs/TASKS.md` 的 `OBS-01-R2` 改判迁出（core 侧已关闭、保留可追溯指针，见其改判记录与 §三 跨仓指针区）。core 侧裁决 **(b)**：`RunBacktest` 返回带 `Status`/`Error` 的小结构，**不给 core `domain.BacktestResult` 加 `Status`**——core `pkg/backtest/contracts/contracts.go:247-264`「ResultSpec 冻结（K9）」明写 `status` 是调用方唯一判据、三值属**响应层**，下沉进 domain 指标集合会造成同一概念两处真相。**core 零改动**，故本单非跨仓原子提交。

- **① 目标**：AI 侧消费者（Copilot 播报、实验日志、`pkg/ai/loop` 候选评估）能读到 `completed` / `invalid` / `failed` 三值状态，不再只靠 `InvalidReasons` 反推。
- **② 上下文**（本仓 + core 冻结契约锚点，开工前逐一读）：
  - 缺陷点：`pkg/ai/client/backtest_client.go:47` 的 `RunBacktest` 返回 `(*domain.BacktestResult, error)`，而响应类型 `BacktestResponse`（`:39`）已解出 `Status string json:"status"`（`:43`）——`btResp.Status` 在 `:78` 被丢掉。`GetBacktestResult`（`:82`）同病（`:103` 解出、返回只 `*domain.BacktestResult`）。
  - 冻结契约（core 仓 `quant-trading`，照此对齐、勿改、勿加第四值）：`pkg/backtest/contracts/contracts.go:247-264` 的 `StatusCompleted` / `StatusInvalid`（InvalidReasons 必填、Error 必空）/ `StatusFailed`（Error 必填、InvalidReasons 必空）；护栏 `resultspec_test.go`。
  - core `pkg/domain/backtest.go:5-39` 的 `BacktestResult` **没有也不该有** `Status`（裁决 (a) 已排除）；`InvalidReasons`（`:38`）由引擎挂，客户端只读不重算。
  - `RunBacktest` 的 3 处调用点：`pkg/ai/agents/validate.go:345`、`pkg/ai/pipeline/strategy.go:99`、`pkg/ai/agents/validate_l4.go:104`（后者经 `httpBacktestRunner` 转 `BacktestMetrics`）。
  - 前端不动：ui 仓 A-07 已按三值接住并自救，7 条单测钉住；本单登记的是**契约残缺**，不是用户可见假成功。
- **③ 要求**：
  1. `RunBacktest` 返回改为小结构（如 `BacktestOutcome{Result *domain.BacktestResult; Status string; Error string}`），三值**原样透传**，不新增取值。
  2. `GetBacktestResult` 同口径改。
  3. 3 处调用点跟进解包；`invalid` 时 `Result.InvalidReasons` 非空（读引擎挂的 Code，不在客户端重算）。
  4. core 仓零改动；不改 `domain.BacktestResult`。
- **④ 约束与非目标**：不改前端；不改 K9 三值语义与字段集；不借机重构 pipeline 结果结构（`BacktestMetrics` 只做最小解包适配）；不加新依赖；不动 HTTP 请求/超时行为。
- **⑤ 验收**：
  1. 单测（`httptest` 假服务端，禁 `time.Sleep` 同步）：同一 `RunBacktest` 对三份响应（`completed` / `invalid`+`invalid_reasons` / `failed`+`error`）分别给出可区分的判读——invalid 时 `InvalidReasons` 非空、failed 时 `Error` 非空且不被误当成功。
  2. 真请求核对：对活 core `POST /api/backtest`，断言线上 JSON `status` 与解出的结构体字段一致。
  3. **破坏验证**：把 `Status` 重新丢掉（只 `return btResp.Result, nil`）→ 三值区分用例当场红 → 还原绿。
  4. `go build ./...`、`go vet ./...`、`gofmt -l`（本单文件）干净；`go test ./pkg/ai/client/... ./pkg/ai/agents/... ./pkg/ai/pipeline/... -race -count=1` 全绿。
- **⑥ 边界**：只补三值透传与调用点解包；前端接线、pipeline 结构重构、`BacktestMetrics` 口径变更另立。
