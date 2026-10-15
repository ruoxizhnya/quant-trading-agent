package main

import (
	"context"
	"encoding/json"
	"github.com/ruoxizhnya/quant-trading/pkg/httpserver"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	aicausal "github.com/ruoxizhnya/quant-trading-agent/pkg/ai/causal"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/loop"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/pipeline"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/search"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/validation"
	"github.com/ruoxizhnya/quant-trading/pkg/backtest"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
)

// verdictSink 把验证器裁决写回实验日志那一行（P2-9 接线）。
//
// 单开一个接口而不塞进 pipeline.ExperimentSink：裁决不是 pipeline 产出的，
// 它是循环控制器在每次尝试**之后**跑的。让 pipeline 去认识验证器，等于把
// 编排层的活塞进能力层 —— 而 pipeline 连尝试失败都要老实记下来，不该再
// 多背一个「什么时候算裁决」的判断。
type verdictSink interface {
	RecordVerdict(ctx context.Context, experimentID int64, verdict json.RawMessage) error
}

// exploreBias 是偏差维（P2-9d）的输入，来自**当前底座的实测状态**而不是
// 猜的。改这两行之前先看 pkg/ai/validation/bias.go 顶部的接线状态注释。
//
// 留空的字段是**真的没查过**，不是"没问题" —— 未评估的子维度不进概率，
// 只给 note。谎称干净比承认没查更危险。
var exploreBias = validation.BiasInput{
	// 行情落在 ohlcv_daily_qfq：前复权。它带一点前视成分（最新价会
	// 反推历史价），所以这一子维度拿 0.85 而不是满分。
	PriceAdjustment: validation.AdjustPre,
	// 池子来源：同步股票列表默认 list_status="L"（当前上市，见
	// cmd/data/sync_handlers.go:90），默认回测池也是手挑的一批**现存**
	// 蓝筹（pkg/ai/agents/validate.go 的 defaultAShareStockPool）。
	// 两者都没有退市票 —— 幸存者偏差是真的存在，不是理论风险。
	// 填了它，每一条裁决都会带上这条 blocking 质疑：这正是 P2-4 那笔债
	// 该有的样子（看得见的债才是债，藏起来的债会变成错误的自信）。
	PoolSource: validation.PoolSourceCurrent,
	// PITVerified / DataLagKnown 留空：只有 equitydeep 链路强制 ann_date，
	// 探索这条链路没查过。于是前视子维度是「未评估」并给 note。
}

// ExploreHandler 是 P1-2 循环控制器的 HTTP 入口。
//
// 控制器本身只是个库，没有这一层它就只是躺在包里的能力 —— 跟 P1-1b 的 sink
// 同一个教训：**加能力不等于接上了线**。
//
// 它同时是 P1-3 观察页的数据源：进度靠 OnAttempt 回调累积，叫停走 Stop。
type ExploreHandler struct {
	// pipeline 用接口而非具体类型：控制器只要求它能 Execute 一次尝试，
	// 这样测试才能注入一个不解析意图、不跑编译校验的替身。
	tryRunner loop.TryRunner
	runner    pipeline.BacktestRunner
	// verdicts 是裁决的落点（P2-9 接线）。nil = 只跑裁决不落库 ——
	// 观测设施不能反过来成为链路的硬依赖。
	verdicts verdictSink
	// narrator 是因果维（P2-9f）的叙述者。nil = 因果维未评估 —— 没讲就是
	// 没讲，不拿剩下五维凑一个"看起来完整"的结论。
	narrator validation.Narrator
	// listing 读引擎当前使用的上市日历（P2-4）。nil 或返回空 = 引擎这次
	// 没能按日在市过滤，偏差维必须照实记上 PoolSourceCurrent。
	listing func() map[string]storage.ListingWindow

	mu   sync.Mutex
	runs map[string]*exploreRun
}

// biasInput 组装这一轮的偏差维输入（P2-4）。
//
// 池子来源**按引擎的实测状态**决定，不再是写死的常量：引擎预热到了上市
// 日历，就说明这次回测真的是「按日在市」取池子，那条 blocking 该消失；
// 预热不到（stocks 表空 / 没连库），就仍然报 PoolSourceCurrent ——
// 这时候债还在，藏起来它就会变成错误的自信。
func (h *ExploreHandler) biasInput() validation.BiasInput {
	in := exploreBias
	if h.listing == nil {
		return in // 无从判断，保持最保守的口径
	}
	windows := h.listing()
	if len(windows) == 0 {
		return in
	}
	in.PoolSource = validation.PoolSourcePointInTime
	in.PoolSize = len(windows)
	for _, w := range windows {
		if w.Delist != nil {
			in.DelistedInPool++
		}
	}
	return in
}

// exploreRun 是一轮进行中（或已结束）的探索。
type exploreRun struct {
	mu       sync.Mutex
	ctrl     *loop.Controller
	cancel   context.CancelFunc
	started  time.Time
	attempts []loop.Attempt
	result   *loop.RunResult
	err      error
	done     bool
}

// StartExploreRequest 启动一轮探索的请求。
type StartExploreRequest struct {
	Description string `json:"description" binding:"required" example:"做一个动量策略"`
	MaxTries    int    `json:"max_tries" example:"50"`
	// LookbackMin / LookbackMax 限定回看窗口的搜索范围；不传则用 5..250。
	LookbackMin int `json:"lookback_min"`
	LookbackMax int `json:"lookback_max"`
}

// AttemptView 是一次尝试的对外视图。
type AttemptView struct {
	Seq          int            `json:"seq"`
	Hypothesis   string         `json:"hypothesis"`
	Params       map[string]any `json:"params"`
	ExperimentID int64          `json:"experiment_id"`
	Sharpe       float64        `json:"sharpe"`
	OK           bool           `json:"ok"`
	Error        string         `json:"error,omitempty"`

	// Verdict 是验证器链对这次尝试的裁决（P2-9）。失败的尝试为 null ——
	// 没有回测结果就没有可被证伪的东西，不给一个看起来正常的裁决。
	Verdict *validation.Verdict `json:"verdict,omitempty"`
}

// ExploreStatusResponse 是一轮探索的实时状态。
type ExploreStatusResponse struct {
	RunID    string        `json:"run_id"`
	Running  bool          `json:"running"`
	Stopped  string        `json:"stopped,omitempty"`
	Attempts []AttemptView `json:"attempts"`
	BestSeq  *int          `json:"best_seq,omitempty"`
}

// NewExploreHandler 构造探索处理器。
func NewExploreHandler(p loop.TryRunner, runner pipeline.BacktestRunner) *ExploreHandler {
	return &ExploreHandler{
		tryRunner: p,
		runner:    runner,
		runs:      make(map[string]*exploreRun),
	}
}

// RegisterExploreRoutes 注册探索路由。
func (h *ExploreHandler) RegisterExploreRoutes(router *gin.RouterGroup) {
	g := router.Group("/explore")
	{
		g.POST("/runs", h.Start)
		g.GET("/runs/:runID", h.Status)
		g.GET("/runs/:runID/stream", h.Stream)
		g.POST("/runs/:runID/stop", h.Stop)
	}
}

// registerExploreRoutes 注册探索路由。
//
// 探索用的 pipeline 实例**必须带上实验日志落点** —— 否则这一路的尝试一行
// 都不会记，而「试了多少次」正是过拟合检测唯一依赖的数字。
func registerExploreRoutes(router *gin.Engine, runner pipeline.BacktestRunner, sink pipeline.ExperimentSink, engine *backtest.Engine) {
	var opts []pipeline.PipelineOption
	if sink != nil {
		opts = append(opts, pipeline.WithExperimentSink(sink))
	}
	handler := NewExploreHandler(pipeline.NewPipeline(opts...), runner)
	// P2-4：上市日历读到什么口径，偏差维就报什么口径。engine 可能为 nil
	//（DB 没配），那种情况下 listing 保持 nil，偏差维退回最保守的
	// PoolSourceCurrent —— 债还在就照实记。
	if engine != nil {
		handler.listing = engine.ListingWindows
	}
	// 实验日志落点若同时能写裁决（*storage.PostgresStore 满足），就把裁决
	// 也接上；不满足就只记日志不记裁决。main.go 里 Store 可能为 nil
	//（DB 没配），那种情况 sink 是 nil 接口，断言自然失败。
	if vs, ok := sink.(verdictSink); ok {
		handler.verdicts = vs
	}
	// 因果维要真调一次模型：没配 key 就不配叙述者，那一维老老实实记成
	// 未评估。配了却调不通也一样 —— ValidateCausal 返回的错误只记日志，
	// 不会把这一轮探索搞挂。
	if c := ai.NewClient(); c.IsConfigured() {
		handler.narrator = aicausal.New(c)
	}
	api := router.Group("/api")
	handler.RegisterExploreRoutes(api)
}

// Start 启动一轮探索，立即返回 run_id —— 一轮可能跑几十上百次尝试，
// 不能让 HTTP 请求挂在那儿等。
func (h *ExploreHandler) Start(c *gin.Context) {
	var req StartExploreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}
	if req.MaxTries <= 0 {
		req.MaxTries = 20
	}
	lbMin, lbMax := req.LookbackMin, req.LookbackMax
	if lbMin <= 0 {
		lbMin = 5
	}
	if lbMax <= lbMin {
		lbMax = 250
	}

	// ⚠️ 刻意不用 c.Request.Context()：响应一返回 gin 就会取消它，异步跑的
	// 这一轮会在第一步就被掐断。这里要的是独立于请求的生命周期。
	ctx, cancel := context.WithCancel(context.Background())

	space := &search.SearchSpace{Params: []search.ParamDef{
		// 参数名必须与意图参数同名，否则覆盖静默失效（见 P1-2b）。
		{Name: "lookback_days", Type: "int", Min: float64(lbMin), Max: float64(lbMax)},
	}}

	runID := uuid.New().String()
	run := &exploreRun{
		ctrl:    loop.NewController(h.tryRunner, loop.NewTPEProposer(space, time.Now().UnixNano()), h.runner),
		cancel:  cancel,
		started: time.Now(),
	}
	h.mu.Lock()
	h.runs[runID] = run
	h.mu.Unlock()

	go func() {
		defer func() {
			run.mu.Lock()
			run.done = true
			run.mu.Unlock()
		}()
		out, err := run.ctrl.Run(ctx, loop.Config{
			RunID:       runID,
			Description: req.Description,
			MaxTries:    req.MaxTries,
			Bias:        h.biasInput(),
			OnAttempt: func(a loop.Attempt) {
				run.mu.Lock()
				run.attempts = append(run.attempts, a)
				run.mu.Unlock()
				// 锁外写库：裁决落盘不该挡住下一次尝试。
				h.recordVerdict(runID, a)
			},
		})
		run.mu.Lock()
		run.result, run.err = out, err
		run.mu.Unlock()

		// 因果维只对最终候选做一次（P2-9f）。
		//
		// 它是六维里唯一要花一次模型调用的一维：每次尝试都做，一轮几十次
		// 探索就是几十次调用，换来的却是几十份"讲给已被淘汰的候选听"的
		// 故事。真正需要人判断的只有最后那个候选 —— 所以放在这里。
		h.reviewBestCausally(ctx, run, runID, req.Description, out)
	}()

	c.JSON(http.StatusOK, gin.H{"run_id": runID, "max_tries": req.MaxTries})
}

// recordVerdict 把一次尝试的裁决写进实验日志那一行。
//
// 写失败只打日志，不中断探索：裁决是对已有结果的评论，评论丢了实验还在，
// 反过来（为了写评论把探索搞挂）才不可接受。
func (h *ExploreHandler) recordVerdict(runID string, a loop.Attempt) {
	if h.verdicts == nil || a.Verdict == nil || a.ExperimentID == 0 {
		return
	}
	b, err := json.Marshal(a.Verdict)
	if err != nil {
		log.Printf("explore run %s seq %d: 裁决序列化失败，未写入日志: %v", runID, a.Seq, err)
		return
	}
	// 刻意不用请求 ctx：它随响应一起取消，而这一轮是异步跑的。
	// 裁决是事后评论，给个短超时比让它无限挂住更合适。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.verdicts.RecordVerdict(ctx, a.ExperimentID, b); err != nil {
		log.Printf("explore run %s seq %d: 裁决未写入实验日志: %v", runID, a.Seq, err)
	}
}

// reviewBestCausally 给本轮最优的候选补上因果维。
//
// 叙述失败（模型没配、调用超时、返回不是 JSON）时保持原样 —— 那一维仍旧是
// 「未评估」，而不是悄悄记成通过。审查是附加价值，不该有能力毁掉一轮探索。
func (h *ExploreHandler) reviewBestCausally(
	ctx context.Context, run *exploreRun, runID, description string, out *loop.RunResult,
) {
	if h.narrator == nil || out == nil || out.Best == nil || out.Best.Verdict == nil {
		return
	}
	var br *domain.BacktestResult
	if out.Best.Result != nil {
		br = out.Best.Result.BacktestResult
	}
	periods := 0
	if br != nil {
		periods = len(br.PortfolioValues)
	}

	c, err := validation.ValidateCausal(ctx, validation.CausalRequest{
		Name:       description,
		Hypothesis: out.Best.Hypothesis,
		Params:     out.Best.Params,
		Periods:    periods,
		Result:     br,
	}, h.narrator)
	if err != nil {
		log.Printf("explore run %s: 因果维未评估（%v）", runID, err)
		return
	}
	if c == nil {
		return
	}

	// 裁决是通过指针共享给 run.attempts 里那一份的，所以改之前必须持锁 ——
	// 观察页正在另一边 poll 同一个对象。
	run.mu.Lock()
	validation.AttachCausal(out.Best.Verdict, c)
	run.mu.Unlock()

	// 库里那份也要跟着更新，否则日志里的裁决永远缺因果维。
	h.recordVerdict(runID, *out.Best)
}

// Status 查一轮探索的实时进度（轮询端点）。
func (h *ExploreHandler) Status(c *gin.Context) {
	runID := c.Param("runID")
	h.mu.Lock()
	run, ok := h.runs[runID]
	h.mu.Unlock()
	if !ok {
		httpserver.Fail(c, http.StatusNotFound, "run not found")
		return
	}
	c.JSON(http.StatusOK, h.statusOf(runID, run))
}

// statusOf 持 run.mu 取一份当前快照。轮询端点与 SSE 共用它，保证两条路推出去
// 的 JSON 一字不差 —— 前端 EventSource 和 poll 能复用同一套类型。
func (h *ExploreHandler) statusOf(runID string, run *exploreRun) ExploreStatusResponse {
	run.mu.Lock()
	defer run.mu.Unlock()

	views := make([]AttemptView, 0, len(run.attempts))
	for _, a := range run.attempts {
		v := AttemptView{
			Seq:          a.Seq,
			Hypothesis:   a.Hypothesis,
			Params:       a.Params,
			ExperimentID: a.ExperimentID,
			OK:           a.OK,
			Verdict:      a.Verdict,
		}
		if a.Result != nil && a.Result.BacktestResult != nil {
			v.Sharpe = a.Result.BacktestResult.SharpeRatio
		}
		if a.Err != nil {
			v.Error = a.Err.Error()
		}
		views = append(views, v)
	}

	resp := ExploreStatusResponse{
		RunID:    runID,
		Running:  !run.done,
		Attempts: views,
	}
	if run.result != nil {
		resp.Stopped = string(run.result.Stopped)
		if run.result.Best != nil {
			seq := run.result.Best.Seq
			resp.BestSeq = &seq
		}
	}
	return resp
}

// Stream 用 SSE 把一轮探索的实时状态推给观察页，替代每 2.5s 一次的轮询：
// 一条长连接只占限流器 1 次请求，从根上把 429 与「真失败」分开（OPS-01）。
//
// 形状复用 core 先例 data-service 的 sseProgressHandler —— 轮询内存快照转推，
// 不为省这几秒引入多订阅 fan-out。
//
// ⚠️ 客户端断线只是**停推**：这一轮用的是 Start 里 context.Background() 派生的
// 独立 cancel（见 handlers_explore.go Start 的注释），绝不因为连接断了就取消
// 探索 —— 叫停只走 POST /runs/:runID/stop。
func (h *ExploreHandler) Stream(c *gin.Context) {
	runID := c.Param("runID")
	h.mu.Lock()
	run, ok := h.runs[runID]
	h.mu.Unlock()
	if !ok {
		httpserver.Fail(c, http.StatusNotFound, "run not found")
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// 节奏逻辑抽进 pumpStream，让「终止 / 断线」语义脱离网络计时可被确定性测试；
	// 这里只负责把 gin 的 SSE 写入接成 emit。
	pumpStream(c.Request.Context(), 2*time.Second,
		func() ExploreStatusResponse { return h.statusOf(runID, run) },
		func(event string, snap ExploreStatusResponse) {
			c.SSEvent(event, snap)
			c.Writer.Flush()
		})
}

// pumpStream 推「progress →（跑完时）complete」直到本轮结束或 ctx 取消。
//
// snapshot 每次取当下状态，emit 写一帧。⚠️ ctx 取消只是**停推**——本函数拿不到
// run 的 cancel，也就无从取消探索，这正是「连接生命周期 ≠ run 生命周期」的结构
// 保证（叫停只走 POST /runs/:runID/stop）。断线由 ctx 感知，无需对写失败另设分支。
//
// 首帧若已跑完就立刻 progress + complete 返回，不留一个空转到超时的连接。
func pumpStream(ctx context.Context, interval time.Duration,
	snapshot func() ExploreStatusResponse,
	emit func(event string, snap ExploreStatusResponse),
) {
	snap := snapshot()
	emit("progress", snap)
	if !snap.Running {
		emit("complete", snap)
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return // 断线只停推，不 cancel run
		case <-ticker.C:
			snap := snapshot()
			emit("progress", snap)
			if !snap.Running {
				emit("complete", snap)
				return
			}
		}
	}
}

// Stop 叫停一轮探索。已跑的不丢 —— 这正是「被叫停的探索」与「跑满的探索」
// 可信度不同的地方，日志里都留着。
func (h *ExploreHandler) Stop(c *gin.Context) {
	h.mu.Lock()
	run, ok := h.runs[c.Param("runID")]
	h.mu.Unlock()
	if !ok {
		httpserver.Fail(c, http.StatusNotFound, "run not found")
		return
	}

	run.ctrl.Stop()
	if run.cancel != nil {
		run.cancel()
	}
	c.JSON(http.StatusOK, gin.H{"run_id": c.Param("runID"), "stopping": true})
}
