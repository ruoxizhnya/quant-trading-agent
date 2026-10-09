package loop

import (
	"fmt"
	"math/rand"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/search"
)

// RandomProposer 在搜索空间里均匀随机采样。
//
// 它是基线，不是凑数的：没有随机对照，就说不清 TPE 到底带来了多少东西 ——
// 「用了优化算法」和「优化算法真的有用」是两回事。
type RandomProposer struct {
	space *search.SearchSpace
	rng   *rand.Rand
}

// NewRandomProposer 构造随机提议者。seed 固定则结果可复现 —— 一轮探索要能
// 重放，随机源就不能是全局的。
func NewRandomProposer(space *search.SearchSpace, seed int64) *RandomProposer {
	return &RandomProposer{space: space, rng: rand.New(rand.NewSource(seed))}
}

// Suggest 无父代 —— 随机采样不基于任何历史，说它「衍生自某次尝试」是假的。
func (p *RandomProposer) Suggest(observed []Observation) Suggestion {
	return Suggestion{
		Params:     sampleUniform(p.space, p.rng),
		ParentSeq:  nil,
		Hypothesis: fmt.Sprintf("随机探索（第 %d 次）", len(observed)+1),
	}
}

// TPEProposer 用 TPE 采样：把已观测的尝试喂给它，在「好参数」附近继续找。
type TPEProposer struct {
	opt   *search.TPEOptimizer
	space *search.SearchSpace
}

// NewTPEProposer 构造 TPE 提议者。前若干次（nStartupTrials）它自己会退化为
// 随机采样 —— 历史太少时拟合出来的分布没有意义。
func NewTPEProposer(space *search.SearchSpace, seed int64) *TPEProposer {
	return &TPEProposer{opt: search.NewTPEOptimizer(seed), space: space}
}

// ⚠️ 参数名必须与意图参数同名（例如 lookback_days），覆盖才会生效。
// 写成 lookback 之类的别名**不会报错**，只是参数进不了表达式 —— 整轮搜索
// 会静默退化成「同一个策略跑 N 遍」。这是本设计里最容易踩的坑，
// 因为错配没有任何信号。
//
// Suggest 的父子语义：TPE 是在「好参数所在区间」里采样，所以说它衍生自
// 当前最优那次尝试。这是近似，不是严格的遗传父代 —— 写清楚免得日后被
// 当成精确谱系去解读。
func (p *TPEProposer) Suggest(observed []Observation) Suggestion {
	// 失败的尝试不喂给 TPE：它的 Value 是 0 而实际含义是「没跑出来」，
	// 喂进去等于告诉算法这组参数值得 0 分，会把分布带偏。
	history := make([]*search.Trial, 0, len(observed))
	for _, o := range observed {
		if !o.OK {
			continue
		}
		history = append(history, &search.Trial{
			ID: o.Seq, Params: o.Params, Value: o.Value, State: "completed",
		})
	}

	parent := bestSeq(observed)
	if parent == nil {
		return Suggestion{
			Params:     p.opt.Suggest(p.space, history),
			Hypothesis: "TPE 起步：还没有成功观测，先随机铺点",
		}
	}
	return Suggestion{
		Params:     p.opt.Suggest(p.space, history),
		ParentSeq:  parent,
		Hypothesis: fmt.Sprintf("TPE：在第 %d 次（当前最优）附近采样", *parent),
	}
}

// bestSeq 返回目前最优尝试的 seq；一次都没成功时为 nil。
func bestSeq(observed []Observation) *int {
	var best *int
	var bestVal float64
	for i := range observed {
		o := &observed[i]
		if !o.OK {
			continue
		}
		if best == nil || o.Value < bestVal {
			seq := o.Seq
			best = &seq
			bestVal = o.Value
		}
	}
	return best
}

// sampleUniform 在搜索空间里均匀采样，支持 int / float / categorical。
func sampleUniform(space *search.SearchSpace, rng *rand.Rand) map[string]any {
	out := make(map[string]any, len(space.Params))
	for _, d := range space.Params {
		switch d.Type {
		case "int":
			span := int(d.Max-d.Min) + 1
			if span < 1 {
				span = 1
			}
			out[d.Name] = int(d.Min) + rng.Intn(span)
		case "categorical":
			if len(d.Choices) > 0 {
				out[d.Name] = d.Choices[rng.Intn(len(d.Choices))]
			}
		default: // float
			out[d.Name] = d.Min + rng.Float64()*(d.Max-d.Min)
		}
	}
	return out
}
