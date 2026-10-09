package search

import (
	"math"
	"testing"
)

// search 包此前一个测试都没有，而 P1-2 动了 Optimize 的内部实现（重构成
// Suggest 的薄封装）。下面两条锁住「行为没变」—— 没有它们，这次重构是裸的。

// TestTPEOptimizer_OptimizeConverges：TPE 该能在 1 维凸问题上逼近最优。
// 固定 seed，结果确定（内部是 rand.New(rand.NewSource(seed))）。
func TestTPEOptimizer_OptimizeConverges(t *testing.T) {
	o := NewTPEOptimizer(42)
	space := &SearchSpace{Params: []ParamDef{{Name: "x", Type: "float", Min: 0, Max: 10}}}

	// 目标：最小化 (x-3)^2，最优点 x=3。
	res := o.Optimize(func(p map[string]interface{}) float64 {
		x := p["x"].(float64)
		return (x - 3) * (x - 3)
	}, space, 60)

	if res.NTrials != 60 {
		t.Errorf("NTrials = %d, want 60", res.NTrials)
	}
	if res.BestTrial == nil {
		t.Fatal("BestTrial 为 nil —— 一次都没跑成功")
	}
	got := res.BestTrial.Params["x"].(float64)
	if math.Abs(got-3) > 1.0 {
		t.Errorf("60 次内应逼近 x=3，实际最优 x=%v（误差 %v）", got, math.Abs(got-3))
	}
	if len(res.AllTrials) != 60 {
		t.Errorf("AllTrials = %d, want 60 —— 每次尝试都要留下记录，失败也不例外", len(res.AllTrials))
	}
}

// TestTPEOptimizer_Suggest：单步采样在两条分支上都得落在搜索空间内。
//
// 前 nStartupTrials 次走随机（历史太少，TPE 建不出有意义的分布），
// 之后走 TPE。两条路都不能越界。
func TestTPEOptimizer_Suggest(t *testing.T) {
	o := NewTPEOptimizer(1)
	space := &SearchSpace{Params: []ParamDef{{Name: "x", Type: "float", Min: 0, Max: 10}}}

	// 无历史 → 随机采样
	p := o.Suggest(space, nil)
	x, ok := p["x"].(float64)
	if !ok {
		t.Fatalf("采样的参数里没有 x: %v", p)
	}
	if x < 0 || x > 10 {
		t.Errorf("随机采样越界: x=%v", x)
	}

	// 历史足够 → 走 TPE 分支
	history := make([]*Trial, 12)
	for i := range history {
		history[i] = &Trial{
			ID:     i,
			Params: map[string]interface{}{"x": float64(i) * 0.8},
			Value:  float64(i),
			State:  "completed",
		}
	}
	p2 := o.Suggest(space, history)
	x2, ok := p2["x"].(float64)
	if !ok {
		t.Fatalf("TPE 采样的参数里没有 x: %v", p2)
	}
	if x2 < 0 || x2 > 10 {
		t.Errorf("TPE 采样越界: x=%v", x2)
	}
}
