package loop

import (
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSpace() *search.SearchSpace {
	return &search.SearchSpace{Params: []search.ParamDef{
		{Name: "lookback", Type: "int", Min: 5, Max: 60},
		{Name: "top_pct", Type: "float", Min: 0.05, Max: 0.5},
		{Name: "universe", Type: "categorical", Choices: []string{"csi300", "csi500"}},
	}}
}

// TestRandomProposer_SamplesWithinSpace：随机采样不能越界 —— 越界的参数会被
// 底座拒掉，那一整次尝试就白费了。
func TestRandomProposer_SamplesWithinSpace(t *testing.T) {
	p := NewRandomProposer(testSpace(), 1)

	for i := 0; i < 50; i++ {
		s := p.Suggest(nil)
		lb := s.Params["lookback"].(int)
		assert.GreaterOrEqual(t, lb, 5)
		assert.LessOrEqual(t, lb, 60)

		tp := s.Params["top_pct"].(float64)
		assert.GreaterOrEqual(t, tp, 0.05)
		assert.LessOrEqual(t, tp, 0.5)

		assert.Contains(t, []string{"csi300", "csi500"}, s.Params["universe"])
		assert.Nil(t, s.ParentSeq, "随机采样不基于历史，不该伪造父代")
	}
}

// TestTPEProposer_ParentIsBestSuccessful：父代要指向当前最优，而且失败的
// 尝试不能被当成父代 —— 一个没跑出来的 0 分最容易被误读成好结果。
func TestTPEProposer_ParentIsBestSuccessful(t *testing.T) {
	p := NewTPEProposer(testSpace(), 1)
	observed := []Observation{
		{Seq: 0, Params: map[string]any{"lookback": 20}, Value: 2.0, OK: true},
		{Seq: 1, Params: map[string]any{"lookback": 30}, Value: 0.5, OK: true}, // 最优
		{Seq: 2, Params: map[string]any{"lookback": 40}, Value: 3.0, OK: true},
		{Seq: 3, Params: map[string]any{"lookback": 50}, Value: 0.0, OK: false}, // 失败
	}

	s := p.Suggest(observed)
	require.NotNil(t, s.ParentSeq)
	assert.Equal(t, 1, *s.ParentSeq, "父代应是当前最优 seq 1，不是失败的 seq 3")
	assert.Contains(t, s.Hypothesis, "1")
	assert.NotEmpty(t, s.Params)
}

// TestTPEProposer_NoSuccessNoParent：一次都没成功时不该有父代 ——
// 没有「好参数」可言，硬指一个父代是编故事。
func TestTPEProposer_NoSuccessNoParent(t *testing.T) {
	p := NewTPEProposer(testSpace(), 1)

	s := p.Suggest([]Observation{{Seq: 0, OK: false}})
	assert.Nil(t, s.ParentSeq)
	assert.NotEmpty(t, s.Params, "就算没有历史也要给出一组参数，否则循环走不下去")
}

// TestTPEProposer_ParamsWithinSpace：TPE 采样同样不能越界。
func TestTPEProposer_ParamsWithinSpace(t *testing.T) {
	p := NewTPEProposer(testSpace(), 7)
	observed := make([]Observation, 12)
	for i := range observed {
		observed[i] = Observation{
			Seq:    i,
			Params: map[string]any{"lookback": 10 + i, "top_pct": 0.1, "universe": "csi300"},
			Value:  float64(i),
			OK:     true,
		}
	}

	for i := 0; i < 20; i++ {
		s := p.Suggest(observed)
		lb, ok := s.Params["lookback"]
		require.True(t, ok, "参数里要有 lookback")
		switch v := lb.(type) {
		case int:
			assert.GreaterOrEqual(t, v, 5)
			assert.LessOrEqual(t, v, 60)
		case float64:
			assert.GreaterOrEqual(t, v, 5.0)
			assert.LessOrEqual(t, v, 60.0)
		}
	}
}
