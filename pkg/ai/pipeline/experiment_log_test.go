package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	yamlgen "github.com/ruoxizhnya/quant-trading-agent/pkg/ai/yaml"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P1-1b：pipeline 是实验日志的**生产者**。
//
// 1a 只建了表和存取方法 —— 没有生产者，库里永远是空的。而空库在验证器
// 眼里等于「没试过」，过拟合检测、路径回放、复盘全都无从谈起。
//
// 这里用内存 sink 而不是真库：要锁的是 pipeline 的行为契约（什么时候写、
// 落成什么状态、带什么参数），不是 Postgres 的存取正确性（那是 1a 的事）。

type completeCall struct {
	id      int64
	metrics *storage.ExperimentMetrics
	errMsg  string
}

// memSink 记录每一次写入，供断言。
type memSink struct {
	inserted  []*storage.Experiment
	completed []completeCall
	insertErr error
	nextID    int64
}

func (m *memSink) InsertExperiment(ctx context.Context, e *storage.Experiment) (int64, error) {
	if m.insertErr != nil {
		return 0, m.insertErr
	}
	m.nextID++
	e.ID = m.nextID // 真实存储会回填自增 ID，这里照做，update 才能按 ID 找到它
	m.inserted = append(m.inserted, e)
	return m.nextID, nil
}

func (m *memSink) UpdateExperiment(ctx context.Context, id int64, u storage.ExperimentUpdate) error {
	for _, e := range m.inserted {
		if e.ID != id {
			continue
		}
		// 与 SQL 侧的 COALESCE 语义一致：零值字段表示「不改」。
		if u.Params != nil {
			e.Params = u.Params
		}
		if u.StrategyName != "" {
			e.StrategyName = u.StrategyName
		}
		if u.Expression != "" {
			e.Expression = u.Expression
		}
		return nil
	}
	return fmt.Errorf("experiment %d not found", id)
}

func (m *memSink) CompleteExperiment(ctx context.Context, id int64, metrics *storage.ExperimentMetrics, errMsg string) error {
	m.completed = append(m.completed, completeCall{id: id, metrics: metrics, errMsg: errMsg})
	return nil
}

func newLoggedPipeline(sink ExperimentSink) *Pipeline {
	return NewPipelineWithDeps(
		intent.NewParser(), yamlgen.NewGenerator(), &ai.MockClient{},
		WithExperimentSink(sink),
	)
}

// TestExecute_LogsExperimentOnSuccess：跑通一次，库里必须留下一行
// completed，且带指标。这是「能回放」的最低要求。
func TestExecute_LogsExperimentOnSuccess(t *testing.T) {
	sink := &memSink{}
	runner := &mockBacktestRunner{result: &domain.BacktestResult{
		TotalTrades: 7, TotalReturn: 0.25, SharpeRatio: 1.8,
	}}

	res, err := newLoggedPipeline(sink).Execute(context.Background(), "做一个动量策略", runner)
	require.NoError(t, err)
	require.Equal(t, StageComplete, res.Status)

	require.Len(t, sink.inserted, 1, "一次尝试 = 一行日志")
	require.Len(t, sink.completed, 1, "跑完必须收尾，不能永远 running")

	// 插入即 running：先落「开始了」这个事实，进程崩了也能看见断在哪。
	assert.Equal(t, storage.ExperimentStatusRunning, sink.inserted[0].Status)

	require.NotNil(t, sink.completed[0].metrics, "成功的尝试必须留下指标")
	assert.InDelta(t, 1.8, sink.completed[0].metrics.SharpeRatio, 1e-9)
	assert.InDelta(t, 0.25, sink.completed[0].metrics.TotalReturn, 1e-9)
	assert.Equal(t, 7, sink.completed[0].metrics.TotalTrades)
	assert.Empty(t, sink.completed[0].errMsg)
}

// TestExecute_LogsFailureWithError：失败也要落行。
//
// 这是整个实验日志最有价值的部分 —— 试 5 次撞出来的和试 500 次撞出来的，
// 同样结果可信度差一个量级，而「试了多少次」只有把失败也记下来才知道。
func TestExecute_LogsFailureWithError(t *testing.T) {
	sink := &memSink{}
	runner := &mockBacktestRunner{shouldFail: true}

	res, err := newLoggedPipeline(sink).Execute(context.Background(), "做一个动量策略", runner)
	require.Error(t, err)
	require.Equal(t, StageFailed, res.Status)

	require.Len(t, sink.inserted, 1, "失败的尝试也必须留下痕迹")
	require.Len(t, sink.completed, 1)

	assert.NotEmpty(t, sink.completed[0].errMsg, "失败原因要能回放出来")
	assert.Nil(t, sink.completed[0].metrics, "失败的尝试不该留下指标")
}

// TestExecute_HonoursExperimentContext：一轮探索里第 n 次尝试的位置由调用方
// 说了算（P1-2 的循环控制器要靠它把 100 次尝试串成一条路径）。
func TestExecute_HonoursExperimentContext(t *testing.T) {
	sink := &memSink{}
	parentID := int64(11)
	ctx := WithExperimentContext(context.Background(), ExperimentContext{
		RunID:        "run-42",
		Seq:          5,
		ParentID:     &parentID,
		Hypothesis:   "父代窗口 20 不错，试试 30",
		DatasetSplit: storage.DatasetSplitHold,
	})

	_, err := newLoggedPipeline(sink).Execute(ctx, "做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err)

	require.Len(t, sink.inserted, 1)
	e := sink.inserted[0]
	assert.Equal(t, "run-42", e.RunID)
	assert.Equal(t, 5, e.Seq)
	require.NotNil(t, e.ParentID)
	assert.Equal(t, int64(11), *e.ParentID)
	assert.Equal(t, "父代窗口 20 不错，试试 30", e.Hypothesis)
	assert.Equal(t, storage.DatasetSplitHold, e.DatasetSplit)
}

// TestExecute_DefaultExperimentContext：不传上下文时的默认行为 ——
// 一次 Execute 自成一轮探索的第 0 次尝试，数据划分按 train 记。
func TestExecute_DefaultExperimentContext(t *testing.T) {
	sink := &memSink{}

	res, err := newLoggedPipeline(sink).Execute(context.Background(), "做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err)

	require.Len(t, sink.inserted, 1)
	e := sink.inserted[0]
	assert.Equal(t, res.ID, e.RunID, "默认用 job ID 作 run ID")
	assert.Equal(t, 0, e.Seq)
	assert.Nil(t, e.ParentID)
	assert.Equal(t, storage.DatasetSplitTrain, e.DatasetSplit)
}

// TestExecute_RecordsWhatWasTried：参数向量与表达式必须留下 ——
// 回放时若只知道结果不知道试了什么，这条路径等于没记。
func TestExecute_RecordsWhatWasTried(t *testing.T) {
	sink := &memSink{}

	_, err := newLoggedPipeline(sink).Execute(context.Background(), "做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err)

	require.Len(t, sink.inserted, 1)
	e := sink.inserted[0]
	assert.NotEmpty(t, e.Expression, "要能看出这次跑的是什么信号")
	assert.NotEmpty(t, e.Params, "参数向量不能是空的")
	assert.NotEmpty(t, e.StrategyName)
}

// TestExecute_NoSinkIsSilent：没配日志落点也要能正常跑完 ——
// 日志是观测设施，不该成为链路的硬依赖。
func TestExecute_NoSinkIsSilent(t *testing.T) {
	p := NewPipelineWithDeps(intent.NewParser(), yamlgen.NewGenerator(), &ai.MockClient{})

	res, err := p.Execute(context.Background(), "做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err)
	assert.Equal(t, StageComplete, res.Status)
}

// TestExecute_SinkFailureDoesNotBreakPipeline：日志写不进去不能把实验本身搞挂。
//
// 但也不能静默 —— 写失败要在 Logs 里留一句，否则「库里没行」会被误读成
// 「没试过」，而实际上是「试过但没记上」，两者的含义完全相反。
func TestExecute_SinkFailureDoesNotBreakPipeline(t *testing.T) {
	sink := &memSink{insertErr: errors.New("db down")}

	res, err := newLoggedPipeline(sink).Execute(context.Background(), "做一个动量策略", &mockBacktestRunner{})
	require.NoError(t, err, "日志写失败不该拖垮实验")
	assert.Equal(t, StageComplete, res.Status)
	assert.True(t, containsLog(res, "实验日志"), "写失败要留可见的痕迹")
}

func containsLog(r *Result, substr string) bool {
	for _, l := range r.Logs {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}
