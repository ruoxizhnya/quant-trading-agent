package loop

import (
	"context"
	"errors"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/pipeline"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P1-2 循环控制器。
//
// 控制器不搜索参数、不评价好坏 —— 那分别是 Proposer 和验证器（P2-9）的事。
// 它只管节奏：分配 seq、把每一步接到链路上、该停就停。所以这里的测试全部
// 是「节奏」方面的：跑满、叫停、失败后继续、位置传对。

func ptr(i int) *int { return &i }

// fakeTryRunner 顶替真实 pipeline：真 pipeline 要解析意图、生成 YAML、跑
// 编译校验，慢且和本层职责无关。它同时记录收到的探索位置 —— seq 传错是那种
// 静默出错、事后才发现路径对不上的问题。
type fakeTryRunner struct {
	calls  []pipeline.ExperimentContext
	sharpe []float64 // 第 i 次尝试返回的 Sharpe
	failAt map[int]bool
	nextID int64
}

func (f *fakeTryRunner) Execute(ctx context.Context, description string, runner pipeline.BacktestRunner) (*pipeline.Result, error) {
	ec := pipeline.ExperimentContextFrom(ctx)
	f.calls = append(f.calls, ec)

	sh := 0.0
	if ec.Seq < len(f.sharpe) {
		sh = f.sharpe[ec.Seq]
	}
	f.nextID++
	res := &pipeline.Result{
		ID:             "job",
		Status:         pipeline.StageComplete,
		ExperimentID:   f.nextID,
		BacktestResult: &domain.BacktestResult{SharpeRatio: sh},
	}
	if f.failAt[ec.Seq] {
		return res, errors.New("backtest exploded")
	}
	return res, nil
}

// scriptedProposer 按脚本给建议，好断言控制器确实照它说的做。
type scriptedProposer struct {
	suggestions []Suggestion
	// onSuggest 在每次建议前调用，用来在循环中途制造取消。
	onSuggest func(observedCount int)
}

func (p *scriptedProposer) Suggest(observed []Observation) Suggestion {
	if p.onSuggest != nil {
		p.onSuggest(len(observed))
	}
	if len(observed) < len(p.suggestions) {
		return p.suggestions[len(observed)]
	}
	return Suggestion{Params: map[string]any{"i": len(observed)}}
}

func newController(r *fakeTryRunner, p Proposer) *Controller {
	return NewController(r, p, nil)
}

// TestRun_ExhaustsMaxTries：跑满配置次数，seq 从 0 连续递增。
func TestRun_ExhaustsMaxTries(t *testing.T) {
	r := &fakeTryRunner{sharpe: []float64{0.5, 1.0, 2.0, 1.5, 0.2}}

	out, err := newController(r, &scriptedProposer{}).Run(context.Background(),
		Config{RunID: "run-1", Description: "做一个动量策略", MaxTries: 5})
	require.NoError(t, err)

	assert.Equal(t, StopExhausted, out.Stopped)
	require.Len(t, out.Tries, 5)
	for i, a := range out.Tries {
		assert.Equal(t, i, a.Seq, "seq 必须连续，路径回放靠它")
	}
	// 目标是最小化 -Sharpe，所以最优应是 Sharpe 最大的 seq 2。
	require.NotNil(t, out.Best)
	assert.Equal(t, 2, out.Best.Seq)
}

// TestRun_CancelledBeforeStart：已经取消的上下文，一次都不该跑。
//
// 中断检查放在循环开头而不是结尾 —— 放结尾会多跑一次，而那一次可能是
// 用户明确叫停之后才启动的。
func TestRun_CancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &fakeTryRunner{}
	out, err := newController(r, &scriptedProposer{}).Run(ctx,
		Config{RunID: "run-1", MaxTries: 5})
	require.NoError(t, err, "被叫停不是错误")

	assert.Equal(t, StopCancelled, out.Stopped)
	assert.Empty(t, out.Tries)
	assert.Empty(t, r.calls)
}

// TestRun_CancelledMidway：中途取消要优雅停下，且已跑的不丢。
func TestRun_CancelledMidway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := &fakeTryRunner{sharpe: []float64{1, 2, 3, 4, 5}}
	p := &scriptedProposer{onSuggest: func(n int) {
		// 第 3 次尝试（seq 2）已经启动，此刻叫停。
		if n == 2 {
			cancel()
		}
	}}

	out, err := newController(r, p).Run(ctx, Config{RunID: "run-1", MaxTries: 10})
	require.NoError(t, err)

	assert.Equal(t, StopCancelled, out.Stopped)
	// 3 次而不是 2 次：取消到达时 seq 2 已经在跑，让它跑完 —— 掐断一半的
	// 尝试状态既非成功也非失败，回放时最难解释。第 4 次不再启动。
	require.Len(t, out.Tries, 3, "叫停后不该再开新的尝试，已进入的那次跑完并保留")
}

// TestStop：主动 Stop 与 ctx 取消等价 —— 观察页的「叫停」按钮走这条路。
func TestStop(t *testing.T) {
	r := &fakeTryRunner{}
	c := newController(r, &scriptedProposer{})
	c.Stop()

	out, err := c.Run(context.Background(), Config{RunID: "run-1", MaxTries: 5})
	require.NoError(t, err)

	assert.Equal(t, StopCancelled, out.Stopped)
	assert.Empty(t, out.Tries)
}

// TestRun_SingleFailureDoesNotStopLoop：一次失败不代表这一轮该完蛋。
//
// 失败本身就是信号 —— 试 5 次和试 500 次撞出来的结果可信度差一个量级，
// 这个数字只有把失败也留着才知道。
func TestRun_SingleFailureDoesNotStopLoop(t *testing.T) {
	r := &fakeTryRunner{
		sharpe: []float64{1, 2, 0, 4, 5},
		failAt: map[int]bool{2: true},
	}

	out, err := newController(r, &scriptedProposer{}).Run(context.Background(),
		Config{RunID: "run-1", MaxTries: 5})
	require.NoError(t, err)

	require.Len(t, out.Tries, 5, "单次失败不该中断整轮")
	require.Error(t, out.Tries[2].Err)
	assert.False(t, out.Tries[2].OK)
	assert.NotZero(t, out.Tries[2].ExperimentID, "失败也要留下日志行 ID")

	assert.True(t, out.Tries[3].OK, "失败之后要继续试")
	assert.True(t, out.Tries[4].OK)

	// 失败的尝试不能当选最优，哪怕它的 Value 恰好很小。
	require.NotNil(t, out.Best)
	assert.NotEqual(t, 2, out.Best.Seq)
}

// TestRun_PropagatesExperimentContext：run_id / seq / hypothesis / split
// 必须真的传到链路上，否则库里的行会对不上号。
func TestRun_PropagatesExperimentContext(t *testing.T) {
	r := &fakeTryRunner{}
	p := &scriptedProposer{suggestions: []Suggestion{
		{Params: map[string]any{"lookback": 20}, Hypothesis: "先试 20 天"},
		{Params: map[string]any{"lookback": 30}, Hypothesis: "20 不够，试 30"},
	}}

	_, err := newController(r, p).Run(context.Background(), Config{
		RunID: "run-42", MaxTries: 2, DatasetSplit: "hold",
	})
	require.NoError(t, err)

	require.Len(t, r.calls, 2)
	assert.Equal(t, "run-42", r.calls[0].RunID)
	assert.Equal(t, 0, r.calls[0].Seq)
	assert.Equal(t, "hold", r.calls[0].DatasetSplit)
	assert.Equal(t, "先试 20 天", r.calls[0].Hypothesis)

	assert.Equal(t, "run-42", r.calls[1].RunID)
	assert.Equal(t, 1, r.calls[1].Seq)
	assert.Equal(t, "20 不够，试 30", r.calls[1].Hypothesis)
}

// TestRun_ParentSeqTranslatedToExperimentID：Proposer 只认得 seq（它看不见
// 库里的 ID），翻译成 experiment ID 是控制器的活。这条链断了，回放时就
// 讲不出「为什么从第 n 步走到第 n+1 步」。
func TestRun_ParentSeqTranslatedToExperimentID(t *testing.T) {
	r := &fakeTryRunner{}
	first := ptr(0)
	p := &scriptedProposer{suggestions: []Suggestion{
		{Params: map[string]any{"lookback": 20}},
		{Params: map[string]any{"lookback": 30}, ParentSeq: first},
	}}

	_, err := newController(r, p).Run(context.Background(), Config{RunID: "run-1", MaxTries: 2})
	require.NoError(t, err)

	require.Len(t, r.calls, 2)
	assert.Nil(t, r.calls[0].ParentID, "首轮没有父代")
	require.NotNil(t, r.calls[1].ParentID, "seq 0 的父代要翻译成库里的 ID")
	assert.Equal(t, int64(1), *r.calls[1].ParentID, "seq 0 那次的行 ID 是 1")
}

// TestRun_ParentSeqUnknownIsDropped：Proposer 给了一个不存在的父代 seq，
// 不能把整轮搞挂 —— 父子关系是元数据，不是关键路径。
func TestRun_ParentSeqUnknownIsDropped(t *testing.T) {
	r := &fakeTryRunner{}
	missing := ptr(99)
	p := &scriptedProposer{suggestions: []Suggestion{{ParentSeq: missing}}}

	out, err := newController(r, p).Run(context.Background(), Config{RunID: "run-1", MaxTries: 1})
	require.NoError(t, err)
	require.Len(t, r.calls, 1)
	assert.Nil(t, r.calls[0].ParentID, "查不到的父代就当没有")
	assert.Len(t, out.Tries, 1)
}

// TestRun_AutoRunID：不指定 run_id 时自己生成一个 —— 一轮探索必须有标识，
// 否则日志散落在各处，回放无从谈起。
func TestRun_AutoRunID(t *testing.T) {
	out, err := newController(&fakeTryRunner{}, &scriptedProposer{}).Run(
		context.Background(), Config{MaxTries: 1})
	require.NoError(t, err)
	assert.NotEmpty(t, out.RunID)
}

// TestRun_MaxTriesMustBePositive：配置错误要显式报错，不能默默什么都不做
// —— 那会被读成「跑完了，一轮 0 次」。
func TestRun_MaxTriesMustBePositive(t *testing.T) {
	_, err := newController(&fakeTryRunner{}, &scriptedProposer{}).Run(
		context.Background(), Config{MaxTries: 0})
	require.Error(t, err)
}

// TestRun_OnAttemptSeesEveryTry：进度回调每次都要调，失败的也不例外。
//
// 这是观察页（P1-3）唯一的实时数据源 —— 漏一次，界面上就少一行，
// 而「少了一行」看起来跟「没跑」一模一样。
func TestRun_OnAttemptSeesEveryTry(t *testing.T) {
	var seqs []int
	var okFlags []bool
	r := &fakeTryRunner{sharpe: []float64{1, 2, 3, 4}, failAt: map[int]bool{2: true}}

	_, err := newController(r, &scriptedProposer{}).Run(context.Background(), Config{
		RunID: "run-1", MaxTries: 4,
		OnAttempt: func(a Attempt) {
			seqs = append(seqs, a.Seq)
			okFlags = append(okFlags, a.OK)
		},
	})
	require.NoError(t, err)

	assert.Equal(t, []int{0, 1, 2, 3}, seqs)
	assert.Equal(t, []bool{true, true, false, true}, okFlags, "失败的尝试也要通知观察者")
}

// TestRun_DefaultDatasetSplit：没指定划分时按 train 记 —— 默认必须显式偏向
// 训练期，而不是留空让人分不清这行到底用的哪份数据。
func TestRun_DefaultDatasetSplit(t *testing.T) {
	r := &fakeTryRunner{}
	_, err := newController(r, &scriptedProposer{}).Run(
		context.Background(), Config{RunID: "run-1", MaxTries: 1})
	require.NoError(t, err)
	require.Len(t, r.calls, 1)
	assert.Equal(t, "train", r.calls[0].DatasetSplit)
}
