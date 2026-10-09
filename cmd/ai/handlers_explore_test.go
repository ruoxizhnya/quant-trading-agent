package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/loop"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/pipeline"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/validation"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExploreRunner 顶替真实 pipeline。block 非 nil 时会卡住每次尝试，
// 用来观察「正在跑」这个中间状态，以及叫停到底管不管用。
type fakeExploreRunner struct {
	mu    sync.Mutex
	seqs  []int
	block chan struct{}
	// periods > 0 时返回带净值曲线的回测结果 —— 稳健维要净值曲线，
	// 只给 Sharpe 的话那一维评估不了，证不出验证器真的跑了。
	periods int
}

func (f *fakeExploreRunner) Execute(ctx context.Context, description string, runner pipeline.BacktestRunner) (*pipeline.Result, error) {
	ec := pipeline.ExperimentContextFrom(ctx)
	f.mu.Lock()
	f.seqs = append(f.seqs, ec.Seq)
	f.mu.Unlock()

	if f.block != nil {
		<-f.block
	}
	br := &domain.BacktestResult{
		SharpeRatio: float64(ec.Seq) * 0.5,
		TotalTrades: 1,
	}
	if f.periods > 0 {
		start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
		eq := 1_000_000.0
		br.PortfolioValues = make([]domain.PortfolioValue, f.periods)
		for i := range br.PortfolioValues {
			eq *= 1.0002
			br.PortfolioValues[i] = domain.PortfolioValue{
				Date: start.AddDate(0, 0, i), TotalValue: eq,
			}
		}
		br.Trades = []domain.Trade{{
			Symbol: "600000.SH", Direction: domain.DirectionLong,
			Quantity: 1000, Price: 50, Timestamp: start,
		}}
	}
	return &pipeline.Result{
		ID:             "job",
		Status:         pipeline.StageComplete,
		ExperimentID:   int64(ec.Seq + 1),
		BacktestResult: br,
	}, nil
}

func (f *fakeExploreRunner) seen() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.seqs))
	copy(out, f.seqs)
	return out
}

func newExploreRouter(h *ExploreHandler) *gin.Engine {
	r := gin.New()
	h.RegisterExploreRoutes(r.Group("/api"))
	return r
}

func postJSON(r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func startRun(t *testing.T, r *gin.Engine, body string) string {
	t.Helper()
	w := postJSON(r, "/api/explore/runs", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	runID, _ := resp["run_id"].(string)
	require.NotEmpty(t, runID)
	return runID
}

// waitDone 轮询直到这一轮跑完（或超时）。异步接口只能这么测。
func waitDone(t *testing.T, r *gin.Engine, runID string) ExploreStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var st ExploreStatusResponse
	for time.Now().Before(deadline) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/explore/runs/"+runID, nil)
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &st))
		if !st.Running {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("一轮探索在 5 秒内没跑完，最后状态: %+v", st)
	return st
}

// TestExploreHandler_StartRunsToCompletion：启动 → 轮询 → 拿完整结果。
func TestExploreHandler_StartRunsToCompletion(t *testing.T) {
	tr := &fakeExploreRunner{}
	r := newExploreRouter(NewExploreHandler(tr, nil))

	runID := startRun(t, r, `{"description":"做一个动量策略","max_tries":3}`)
	st := waitDone(t, r, runID)

	assert.Equal(t, runID, st.RunID)
	assert.Equal(t, string(loop.StopExhausted), st.Stopped)
	require.Len(t, st.Attempts, 3)
	for i, a := range st.Attempts {
		assert.Equal(t, i, a.Seq)
		assert.True(t, a.OK)
		assert.NotZero(t, a.ExperimentID, "每次尝试都要落到实验日志里")
	}
	require.NotNil(t, st.BestSeq)
	assert.Equal(t, 2, *st.BestSeq, "Sharpe 递增，最优应是最后一次")
	assert.Equal(t, []int{0, 1, 2}, tr.seen())
}

// TestExploreHandler_StopMidway：叫停要真的停 —— 这是观察页那个按钮的意义。
func TestExploreHandler_StopMidway(t *testing.T) {
	tr := &fakeExploreRunner{block: make(chan struct{})}
	r := newExploreRouter(NewExploreHandler(tr, nil))

	runID := startRun(t, r, `{"description":"做一个动量策略","max_tries":50}`)

	// 等第一次尝试真的卡进去，再叫停。
	require.Eventually(t, func() bool { return len(tr.seen()) == 1 }, 2*time.Second, 10*time.Millisecond)

	w := postJSON(r, "/api/explore/runs/"+runID+"/stop", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	close(tr.block) // 放开当前这次，让循环走到下一步的检查点
	st := waitDone(t, r, runID)

	assert.Equal(t, string(loop.StopCancelled), st.Stopped)
	assert.Len(t, st.Attempts, 1, "叫停后不该再开新的尝试")
	assert.Len(t, tr.seen(), 1, "底层也只该被调用一次")
}

// TestExploreHandler_UnknownRunID：查一个不存在的 run 要 404，不是空结果 ——
// 「没这个 run」和「这个 run 一次都没跑」是两回事。
func TestExploreHandler_UnknownRunID(t *testing.T) {
	r := newExploreRouter(NewExploreHandler(&fakeExploreRunner{}, nil))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/explore/runs/nope", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// fakeVerdictSink 顶替 *storage.PostgresStore，记录写进来的裁决。
type fakeVerdictSink struct {
	mu    sync.Mutex
	calls map[int64]json.RawMessage
}

func (f *fakeVerdictSink) RecordVerdict(ctx context.Context, id int64, v json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[int64]json.RawMessage{}
	}
	f.calls[id] = v
	return nil
}

// TestExploreHandler_VerdictSurfacedAndPersisted：P2-9 接线的端到端取证。
//
// 前五片校验器写完之后全仓零调用 —— 这个测试守的是「裁决真的到了前端、
// 也真的进了实验日志」，少任何一截都算没接上。
func TestExploreHandler_VerdictSurfacedAndPersisted(t *testing.T) {
	tr := &fakeExploreRunner{periods: 300}
	h := NewExploreHandler(tr, nil)
	sink := &fakeVerdictSink{}
	h.verdicts = sink
	r := newExploreRouter(h)

	runID := startRun(t, r, `{"description":"做一个动量策略","max_tries":3}`)
	st := waitDone(t, r, runID)

	require.Len(t, st.Attempts, 3)
	for i, a := range st.Attempts {
		require.NotNil(t, a.Verdict, "第 %d 次尝试没有裁决 —— 验证器没接上", i)
		assert.NotEmpty(t, a.Verdict.Challenges, "第 %d 次的质疑清单是空的", i)
	}

	// exploreBias 必须真的传下去：池子按当前上市名单取 = 幸存者偏差，
	// 偏差维要被评估，而且要给出 blocking 质疑。
	_, ok := st.Attempts[0].Verdict.Dimensions[validation.DimensionBias]
	require.True(t, ok, "偏差维未评估：dimensions=%v", st.Attempts[0].Verdict.Dimensions)
	require.NotNil(t, st.Attempts[0].Verdict.BiasResult)
	// 幸存者子维度按地板值走；整个偏差维是已评估子维度的几何平均
	//（幸存者 0.02 × 复权 0.85），所以看子维度而不是综合值。
	assert.Less(t, st.Attempts[0].Verdict.BiasResult.Survivorship, 0.05)
	assert.Positive(t, st.Attempts[0].Verdict.Blocking, "幸存者偏差应是 blocking 级质疑")

	// 落库：每次尝试一行，且内容是能读的 JSON。
	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.calls, 3)
	for id, raw := range sink.calls {
		var v validation.Verdict
		require.NoErrorf(t, json.Unmarshal(raw, &v), "实验 %d 的裁决不是合法 JSON", id)
		assert.NotEmpty(t, v.Challenges)
		assert.NotEmpty(t, v.Weakest, "应该指出最弱的那一维")
	}
}

func floatPtr(v float64) *float64 { return &v }

// fakeNarrator 顶替真 LLM（P2-9f）。它顺便记录自己**看到了什么** ——
// 因果维的立身之本是「预测在看到结果之前做出」，所以叙述者拿到回测结果
// 这件事本身就是 bug。
type fakeNarrator struct {
	theory    *validation.CausalTheory
	err       error
	calls     int
	sawResult bool
}

func (f *fakeNarrator) Narrate(ctx context.Context, req validation.CausalRequest) (*validation.CausalTheory, error) {
	f.calls++
	if req.Result != nil {
		f.sawResult = true
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.theory, nil
}

// TestExploreHandler_CausalAttachedToBestOnly：因果维（P2-9f）的接线取证。
//
// 它是六维里唯一要花一次模型调用的一维，所以节奏是「一轮只给最终候选做
// 一次」。这个测试守三件事：真的跑了、只跑了一次、只挂在最优那一次上。
func TestExploreHandler_CausalAttachedToBestOnly(t *testing.T) {
	tr := &fakeExploreRunner{periods: 300}
	h := NewExploreHandler(tr, nil)
	sink := &fakeVerdictSink{}
	h.verdicts = sink
	n := &fakeNarrator{theory: &validation.CausalTheory{
		Mechanism: "动量：涨得好的票短期继续涨",
		Predictions: []validation.Prediction{
			{Kind: validation.PredictionTradeCount, Statement: "该有几十笔成交",
				Min: floatPtr(10), Max: floatPtr(200)},
		},
	}}
	h.narrator = n
	r := newExploreRouter(h)

	runID := startRun(t, r, `{"description":"做一个动量策略","max_tries":3}`)
	st := waitDone(t, r, runID)

	require.Equal(t, 1, n.calls, "一轮探索只该做一次因果审查")
	assert.False(t, n.sawResult, "回测结果被递给了叙述者 —— 那样它只会照着结果编故事")

	require.NotNil(t, st.BestSeq)
	var best *AttemptView
	for i := range st.Attempts {
		if st.Attempts[i].Seq == *st.BestSeq {
			best = &st.Attempts[i]
		}
	}
	require.NotNil(t, best)
	require.NotNil(t, best.Verdict)
	require.NotNil(t, best.Verdict.Causal, "最优候选应补上因果维")
	assert.Positive(t, best.Verdict.Causal.Testable, "预测应当是可验证的")

	// 落库那份也要跟着更新 —— 日志里的裁决不该永远缺一维。
	sink.mu.Lock()
	raw, ok := sink.calls[best.ExperimentID]
	sink.mu.Unlock()
	require.True(t, ok)
	var v validation.Verdict
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotNil(t, v.Causal, "写进日志的裁决缺因果维")
}

// TestExploreHandler_CausalFailureLeavesUnassessed：模型调不通时因果维是
// **未评估**，不是通过。审查是附加价值，它也不该有能力把探索搞挂。
func TestExploreHandler_CausalFailureLeavesUnassessed(t *testing.T) {
	tr := &fakeExploreRunner{periods: 300}
	h := NewExploreHandler(tr, nil)
	h.narrator = &fakeNarrator{err: context.DeadlineExceeded}
	r := newExploreRouter(h)

	runID := startRun(t, r, `{"description":"做一个动量策略","max_tries":2}`)
	st := waitDone(t, r, runID)

	require.Len(t, st.Attempts, 2, "因果审查失败不该影响探索本身")
	for i, a := range st.Attempts {
		require.NotNil(t, a.Verdict, "第 %d 次没有裁决", i)
		assert.Nil(t, a.Verdict.Causal, "第 %d 次的因果维应是未评估，不是通过", i)
	}
}

// TestExploreHandler_RejectsMissingDescription：缺描述要 400 ——
// 没有方向就没法探索，默默用默认值跑是在浪费一整轮。
func TestExploreHandler_RejectsMissingDescription(t *testing.T) {
	r := newExploreRouter(NewExploreHandler(&fakeExploreRunner{}, nil))

	w := postJSON(r, "/api/explore/runs", `{"max_tries":3}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// P2-4：偏差维的池子来源必须跟引擎的实际口径一致。
//
// 写死 PoolSourceCurrent 会让每一条裁决都带一条 blocking 质疑 —— 那在
// P2-4 修好之前是对的（看得见的债才是债）。但修好之后还这么报就是假警报，
// 而假警报比没警报更糟：它会训练人忽略质疑清单。

func TestExploreBias_NoListingCalendarKeepsCurrentPool(t *testing.T) {
	h := &ExploreHandler{}
	in := h.biasInput()
	if in.PoolSource != validation.PoolSourceCurrent {
		t.Fatalf("没有上市日历时必须报 PoolSourceCurrent，got %q", in.PoolSource)
	}
	// 引擎返回空日历（stocks 表为空 / 没连库）同样算没修好。
	h.listing = func() map[string]storage.ListingWindow { return nil }
	if got := h.biasInput().PoolSource; got != validation.PoolSourceCurrent {
		t.Fatalf("空日历时仍须报 current，got %q", got)
	}
}

func TestExploreBias_ListingCalendarSwitchesToPointInTime(t *testing.T) {
	delist := time.Now().AddDate(-1, 0, 0)
	h := &ExploreHandler{
		listing: func() map[string]storage.ListingWindow {
			return map[string]storage.ListingWindow{
				"600519.SH": {List: time.Now().AddDate(-10, 0, 0)},
				"600001.SH": {List: time.Now().AddDate(-20, 0, 0), Delist: &delist},
			}
		},
	}

	in := h.biasInput()
	if in.PoolSource != validation.PoolSourcePointInTime {
		t.Fatalf("引擎真的按日在市过滤了，就该报 point_in_time，got %q", in.PoolSource)
	}
	if in.PoolSize != 2 {
		t.Errorf("PoolSize = %d, want 2", in.PoolSize)
	}
	if in.DelistedInPool != 1 {
		t.Errorf("DelistedInPool = %d, want 1", in.DelistedInPool)
	}
}
