package loop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/pipeline"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OBS-01：无效运行（空票池 / 0 成交）**不得**被控制器当作成功观测。
//
// 它是「伪造观测不得计入 NumTrials」的落点：一旦把无效运行当成功，
// 多重检验校正（statistical.go 的 adjP=1-(1-rawP)^N）算出的 p 值就是假的。
//
// 这里注入 fake 结果，不碰真库 —— 要验证的是控制器对 InvalidReasons 的反应。

// validityTryRunner 对指定 seq 返回带 InvalidReasons 的结果，其余返回正常结果。
type validityTryRunner struct {
	invalidSeqs map[int]bool
	periods     int
	nextID      int64
}

func (f *validityTryRunner) Execute(ctx context.Context, description string, runner pipeline.BacktestRunner) (*pipeline.Result, error) {
	ec := pipeline.ExperimentContextFrom(ctx)
	f.nextID++

	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	values := make([]domain.PortfolioValue, f.periods)
	eq := 1_000_000.0
	for i := range values {
		eq *= 1.0003
		values[i] = domain.PortfolioValue{Date: start.AddDate(0, 0, i), TotalValue: eq}
	}

	bt := &domain.BacktestResult{
		StartDate:       start,
		TotalReturn:     0.20,
		SharpeRatio:     1.2,
		TotalTrades:     5,
		PortfolioValues: values,
		UniverseMaxSize: ptr(30),
	}
	if f.invalidSeqs[ec.Seq] {
		bt.TotalTrades = 0
		bt.UniverseMaxSize = ptr(0)
		bt.InvalidReasons = []string{"empty_universe", "zero_trades"}
	}

	return &pipeline.Result{
		ID:             "job",
		Status:         pipeline.StageComplete,
		ExperimentID:   f.nextID,
		BacktestResult: bt,
	}, nil
}

// observedSpy 记录每次 Suggest 收到的 observed 快照。
type observedSpy struct {
	snaps [][]Observation
}

func (s *observedSpy) Suggest(observed []Observation) Suggestion {
	cp := make([]Observation, len(observed))
	copy(cp, observed)
	s.snaps = append(s.snaps, cp)
	return Suggestion{Params: map[string]any{"i": len(observed)}}
}

func TestRun_InvalidRunIsNotAnObservation(t *testing.T) {
	// seq 0/1/2 无效，seq 3 有效 —— 这样既能看到「无效不进 history」，
	// 又能让最后一次有效尝试的 NumTrials/FailedTrials 反映出 3 次失败。
	r := &validityTryRunner{
		invalidSeqs: map[int]bool{0: true, 1: true, 2: true},
		periods:     60,
	}
	spy := &observedSpy{}

	out, err := NewController(r, spy, nil).Run(context.Background(), Config{
		RunID: "obs01", Description: "做一个动量策略", MaxTries: 4,
	})
	require.NoError(t, err)
	require.Len(t, out.Tries, 4)

	// 无效运行：OK=false、无 Verdict（没有可被证伪的东西）。
	for i := 0; i < 3; i++ {
		assert.Falsef(t, out.Tries[i].OK, "seq %d 是无效运行，不该 OK", i)
		assert.Nilf(t, out.Tries[i].Verdict, "seq %d 是无效运行，不该有裁决", i)
	}
	// 有效运行：OK=true、有 Verdict。
	assert.True(t, out.Tries[3].OK, "seq 3 是正常回测，应 OK")
	require.NotNil(t, out.Tries[3].Verdict)

	// Best 只能是有效那次 —— 无效运行的 Value 不参与评选。
	require.NotNil(t, out.Best)
	assert.Equal(t, 3, out.Best.Seq, "最优必须是有效运行，而不是被伪造观测污染的那次")

	// 「不计入成功观测」：第 4 次 Suggest 收到的 observed 里，前三次 OK 都是 false。
	require.GreaterOrEqual(t, len(spy.snaps), 4)
	last := spy.snaps[3]
	require.Len(t, last, 3)
	for i := 0; i < 3; i++ {
		assert.Falsef(t, last[i].OK, "第 %d 次尝试在 observed 里不该是成功", i)
	}

	// 「计入 failed」：最后一次有效尝试的统计维应看到 3/4 是失败的。
	sawFailedRatio := false
	for _, c := range out.Tries[3].Verdict.Challenges {
		if strings.Contains(c.Message, "3/4 次尝试是失败的") {
			sawFailedRatio = true
		}
	}
	assert.True(t, sawFailedRatio,
		"无效运行必须计入 FailedTrials（期望统计维质疑里出现 3/4），质疑清单：%+v",
		out.Tries[3].Verdict.Challenges)
}
