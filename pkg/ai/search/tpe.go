package search

import (
	"math"
	"math/rand"
	"sort"
	"sync"
)

// TPEOptimizer implements Tree-structured Parzen Estimator for Bayesian optimization.
type TPEOptimizer struct {
	rng            *rand.Rand
	gamma          float64 // Quantile for splitting observations
	nStartupTrials int
	mu             sync.Mutex
}

// NewTPEOptimizer creates a new TPE optimizer.
func NewTPEOptimizer(seed int64) *TPEOptimizer {
	return &TPEOptimizer{
		rng:            rand.New(rand.NewSource(seed)),
		gamma:          0.25,
		nStartupTrials: 10,
	}
}

// SearchSpace defines the parameter search space.
type SearchSpace struct {
	Params []ParamDef `json:"params"`
}

// ParamDef defines a single parameter.
type ParamDef struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"` // "int", "float", "categorical"
	Min     float64  `json:"min"`
	Max     float64  `json:"max"`
	Choices []string `json:"choices,omitempty"`
}

// Trial represents a single optimization trial.
type Trial struct {
	ID     int                    `json:"id"`
	Params map[string]interface{} `json:"params"`
	Value  float64                `json:"value"`
	State  string                 `json:"state"` // "running", "completed", "failed"
}

// OptimizeResult holds the optimization result.
type OptimizeResult struct {
	BestTrial *Trial   `json:"best_trial"`
	AllTrials []*Trial `json:"all_trials"`
	NTrials   int      `json:"n_trials"`
	BestValue float64  `json:"best_value"`
}

// Suggest 给出下一组待试参数（单步采样）。
//
// 为什么要有它：原先只有一个跑到底的 Optimize，中间插不进任何动作 ——
// 而循环控制器（P1-2）每一步都要分配 seq、写实验日志、检查该不该中断，
// 这些只有控制器知道。把采样这一步单独暴露出来，控制器才能一步一步地驱动。
//
// 前 nStartupTrials 次用随机采样：TPE 要拟合「好参数长什么样」，历史太少时
// 建不出有意义的分布，硬采样只会过拟合到噪声上。
func (o *TPEOptimizer) Suggest(space *SearchSpace, history []*Trial) map[string]interface{} {
	if len(history) < o.nStartupTrials {
		return o.sampleRandom(space)
	}
	return o.sampleTPE(space, history)
}

// Optimize runs TPE optimization.
//
// 现在只是 Suggest 的薄封装 —— 循环逻辑与采样逻辑分开，控制器才能复用同一份
// 采样而自己掌握节奏。
func (o *TPEOptimizer) Optimize(objective func(map[string]interface{}) float64, space *SearchSpace, nTrials int) *OptimizeResult {
	result := &OptimizeResult{
		AllTrials: make([]*Trial, 0, nTrials),
		BestValue: math.Inf(1),
	}

	for i := 0; i < nTrials; i++ {
		trial := &Trial{
			ID:     i,
			Params: make(map[string]interface{}),
			State:  "running",
		}

		trial.Params = o.Suggest(space, result.AllTrials)

		// Evaluate objective
		value := objective(trial.Params)
		trial.Value = value
		trial.State = "completed"

		result.AllTrials = append(result.AllTrials, trial)

		// Update best
		if value < result.BestValue {
			result.BestValue = value
			result.BestTrial = trial
		}
	}

	result.NTrials = nTrials
	return result
}

// sampleRandom samples parameters randomly from the search space.
func (o *TPEOptimizer) sampleRandom(space *SearchSpace) map[string]interface{} {
	params := make(map[string]interface{})
	for _, param := range space.Params {
		params[param.Name] = o.sampleParam(param)
	}
	return params
}

// sampleTPE samples parameters using TPE algorithm.
func (o *TPEOptimizer) sampleTPE(space *SearchSpace, trials []*Trial) map[string]interface{} {
	// Split observations into good and bad based on quantile
	n := len(trials)
	nGood := int(math.Max(1, float64(n)*o.gamma))

	// Sort trials by value
	sorted := make([]*Trial, len(trials))
	copy(sorted, trials)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Value < sorted[j].Value
	})

	goodTrials := sorted[:nGood]
	badTrials := sorted[nGood:]

	params := make(map[string]interface{})
	for _, param := range space.Params {
		params[param.Name] = o.sampleParamTPE(param, goodTrials, badTrials)
	}
	return params
}

// sampleParam samples a single parameter randomly.
func (o *TPEOptimizer) sampleParam(param ParamDef) interface{} {
	switch param.Type {
	case "int":
		return o.rng.Intn(int(param.Max-param.Min)+1) + int(param.Min)
	case "float":
		return o.rng.Float64()*(param.Max-param.Min) + param.Min
	case "categorical":
		if len(param.Choices) > 0 {
			return param.Choices[o.rng.Intn(len(param.Choices))]
		}
		return ""
	default:
		return 0
	}
}

// sampleParamTPE samples a parameter using TPE's density estimation.
func (o *TPEOptimizer) sampleParamTPE(param ParamDef, goodTrials, badTrials []*Trial) interface{} {
	switch param.Type {
	case "int", "float":
		return o.sampleNumericTPE(param, goodTrials, badTrials)
	case "categorical":
		return o.sampleCategoricalTPE(param, goodTrials, badTrials)
	default:
		return 0
	}
}

// sampleNumericTPE samples a numeric parameter using kernel density estimation.
func (o *TPEOptimizer) sampleNumericTPE(param ParamDef, goodTrials, badTrials []*Trial) float64 {
	// Extract values from good trials
	var goodValues []float64
	for _, trial := range goodTrials {
		if v, ok := trial.Params[param.Name].(float64); ok {
			goodValues = append(goodValues, v)
		}
		if v, ok := trial.Params[param.Name].(int); ok {
			goodValues = append(goodValues, float64(v))
		}
	}

	if len(goodValues) == 0 {
		return o.rng.Float64()*(param.Max-param.Min) + param.Min
	}

	// Sample from good observations with small noise
	idx := o.rng.Intn(len(goodValues))
	value := goodValues[idx]

	// Add Gaussian noise for exploration
	std := (param.Max - param.Min) / 10.0
	noise := o.rng.NormFloat64() * std
	value += noise

	// Clip to bounds
	value = math.Max(param.Min, math.Min(param.Max, value))

	if param.Type == "int" {
		return float64(int(value + 0.5))
	}
	return value
}

// sampleCategoricalTPE samples a categorical parameter.
func (o *TPEOptimizer) sampleCategoricalTPE(param ParamDef, goodTrials, badTrials []*Trial) string {
	if len(param.Choices) == 0 {
		return ""
	}

	// Count occurrences in good trials
	counts := make(map[string]int)
	for _, trial := range goodTrials {
		if v, ok := trial.Params[param.Name].(string); ok {
			counts[v]++
		}
	}

	// Weighted random selection
	if len(counts) > 0 {
		var weighted []struct {
			choice string
			weight float64
		}
		totalWeight := 0.0
		for _, choice := range param.Choices {
			weight := float64(counts[choice]) + 0.1 // Add small constant for smoothing
			weighted = append(weighted, struct {
				choice string
				weight float64
			}{choice, weight})
			totalWeight += weight
		}

		r := o.rng.Float64() * totalWeight
		for _, w := range weighted {
			r -= w.weight
			if r <= 0 {
				return w.choice
			}
		}
	}

	return param.Choices[o.rng.Intn(len(param.Choices))]
}
