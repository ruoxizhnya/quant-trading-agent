package builtin

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/drift"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy/monitor"
)

// ═══════════════════════════════════════════════════════════════════════
//  StrategyHealthTool (P2-6)
// ═══════════════════════════════════════════════════════════════════════
//
// 回答：**这个策略是不是开始不行了？**
//
// pkg/ai/drift（概念漂移检测）和 pkg/strategy/monitor（策略健康监控）此前
// 是两个零调用方的孤儿包 —— 实现完整，但没人消费（TASKS P2-6）。这个工具
// 把它们接起来：monitor 负责滚动指标与告警，drift 负责判断**收益序列的
// 分布**有没有变（均值漂移 / 方差漂移 / 分布漂移）。
//
// 为什么和验证器链的「稳健维」不重复：稳健维看的是**参数邻域塌不塌**和
// **分年度一不一致**，是回测内部的性质；这里看的是**时间序列上最近的
// 表现有没有偏离历史**，是上线之后才会出现的问题。
//
// 无状态调用：每次 Execute 新建 monitor、喂完整段序列、读一次告警。
// monitor 本身是有状态的，但工具不该在两次调用之间偷偷留状态 —— 那会让
// 同样的输入在第二次调用得到不同答案。
//
// Tool name: "monitor.strategy_health"
// Input: series (required), strategy (optional)
// Output: *strategyHealthResult

// strategyHealthResult 是工具的输出。
//
// Alerts 是结构化的告警清单；Drift 单独列出，因为「漂移」和「指标破线」
// 是两类不同的信号 —— 前者说"这个策略可能失效了"，后者说"这个策略现在
// 表现不好"，处置方式不一样。
type strategyHealthResult struct {
	Strategy        string         `json:"strategy"`
	Points          int            `json:"points"`
	RollingSharpe   float64        `json:"rolling_sharpe"`
	RollingMaxDD    float64        `json:"rolling_max_dd"`
	WinRate         float64        `json:"win_rate"`
	ConsecutiveLoss int            `json:"consecutive_losses"`
	Drift           []driftFinding `json:"drift"`
	DriftDetected   bool           `json:"drift_detected"`
	Alerts          []alertView    `json:"alerts"`
	Verdict         string         `json:"verdict"`
	Thresholds      thresholdView  `json:"thresholds"`
}

type driftFinding struct {
	Type      string  `json:"type,omitempty"`
	Severity  string  `json:"severity,omitempty"`
	Message   string  `json:"message,omitempty"`
	Statistic float64 `json:"statistic"`
}

type alertView struct {
	Type      string  `json:"type"`
	Severity  string  `json:"severity"`
	Message   string  `json:"message"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

type thresholdView struct {
	MinSharpe            float64 `json:"min_sharpe"`
	MaxDrawdown          float64 `json:"max_drawdown"`
	ConsecutiveLossLimit int     `json:"consecutive_loss_limit"`
	WindowSize           int     `json:"window_size"`
}

// driftSignificance 是漂移检测的显著性水平。
//
// 注意 drift.NewDetector 的第二个参数是 **p 值阈值**不是统计量阈值：
// 判定为 `pValue < threshold`。传 2.0 会让一切都判成漂移（P2-6 踩过），
// 这里必须是 0 到 1 之间的小数。
const driftSignificance = 0.05

// driftAdapter 把 *drift.Detector 适配成 monitor.DriftDetector。
//
// monitor 故意不 import pkg/ai/drift（否则形成 monitor → drift 的反向依赖），
// 而是在本地定义了只含 5 个字段的 DriftResult。这个 adapter 就是那道桥 ——
// 它在组合根（工具层）完成转换，两个包都不用知道对方。
type driftAdapter struct {
	det *drift.Detector
}

func (a driftAdapter) DetectAll(values []float64) ([]*monitor.DriftResult, error) {
	if a.det == nil {
		return nil, fmt.Errorf("drift detector is nil")
	}
	raw, err := a.det.DetectAll(values)
	if err != nil {
		return nil, err
	}
	out := make([]*monitor.DriftResult, 0, len(raw))
	for _, r := range raw {
		if r == nil {
			continue
		}
		out = append(out, &monitor.DriftResult{
			DriftDetected: r.DriftDetected,
			DriftType:     r.DriftType,
			Severity:      r.Severity,
			Statistic:     r.Statistic,
			Message:       r.Message,
		})
	}
	return out, nil
}

// StrategyHealthTool 检查一段策略表现序列是否出现退化或漂移。
type StrategyHealthTool struct{}

var _ tools.Tool = (*StrategyHealthTool)(nil)

func NewStrategyHealthTool() *StrategyHealthTool { return &StrategyHealthTool{} }

func (t *StrategyHealthTool) Name() string { return "monitor.strategy_health" }

func (t *StrategyHealthTool) Description() string {
	return "Check whether a strategy is degrading: computes rolling Sharpe, max drawdown, " +
		"win rate and consecutive losses, AND runs concept-drift detection (mean / variance / " +
		"distribution shift) on the return series. " +
		"Use this to answer 'is this strategy still working?' — a strategy can have a good " +
		"backtest Sharpe and still be drifting out of regime. " +
		"Feed at least 2x the window size (default 60) of daily points for drift detection to be meaningful."
}

func (t *StrategyHealthTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name: "series",
			Type: "array",
			Description: "Daily performance points, oldest first. Each item: " +
				"{\"daily_return\": 0.01, \"equity\": 1000000}. daily_return is a fraction (0.01 = +1%).",
			Required: true,
		},
		{
			Name:        "strategy",
			Type:        "string",
			Description: "Strategy name (used in alerts). Defaults to 'ad-hoc'.",
			Required:    false,
			Default:     "ad-hoc",
		},
	}
}

func (t *StrategyHealthTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Strategy health: rolling metrics, drift findings, and structured alerts.",
		Fields: []tools.OutputField{
			{Name: "rolling_sharpe", Type: "float", Description: "Rolling Sharpe over the window."},
			{Name: "rolling_max_dd", Type: "float", Description: "Rolling max drawdown over the window."},
			{Name: "win_rate", Type: "float", Description: "Fraction of days with non-negative return."},
			{Name: "drift_detected", Type: "bool", Description: "True if any drift test fired."},
			{Name: "drift", Type: "array", Description: "Drift findings: type, severity, statistic."},
			{Name: "alerts", Type: "array", Description: "Structured alerts (Sharpe / drawdown / consecutive loss / drift)."},
			{Name: "verdict", Type: "string", Description: "One-line human-readable summary."},
		},
	}
}

// pointSeries 是输入序列的一项。
type pointSeries struct {
	DailyReturn float64 `json:"daily_return"`
	Equity      float64 `json:"equity"`
}

func (t *StrategyHealthTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	_ = ctx

	name, err := optionalString(args, "strategy")
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = "ad-hoc"
	}

	raw, ok := args["series"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("%w: series is required", tools.ErrInvalidArgs)
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: series must be an array", tools.ErrInvalidArgs)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: series is empty — 没有数据就谈不上健康与否", tools.ErrInvalidArgs)
	}

	points := make([]pointSeries, 0, len(items))
	for i, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%w: series[%d] must be an object", tools.ErrInvalidArgs, i)
		}
		var p pointSeries
		if v, ok := m["daily_return"]; ok {
			p.DailyReturn, err = toFloat(v)
			if err != nil {
				return nil, fmt.Errorf("%w: series[%d].daily_return: %v", tools.ErrInvalidArgs, i, err)
			}
		}
		if v, ok := m["equity"]; ok {
			p.Equity, err = toFloat(v)
			if err != nil {
				return nil, fmt.Errorf("%w: series[%d].equity: %v", tools.ErrInvalidArgs, i, err)
			}
		}
		points = append(points, p)
	}

	thresholds := monitor.DefaultAlertThresholds()
	m := monitor.NewStrategyMonitor(thresholds, zerolog.Nop())
	// drift.Detector 需要 window*2 个样本才给结论（见 NewDetector）。
	m.SetDriftDetector(driftAdapter{det: drift.NewDetector(monitor.DefaultWindowSize, driftSignificance)})
	m.Register(name)

	for _, p := range points {
		if err := m.Update(name, p.DailyReturn, p.Equity); err != nil {
			return nil, fmt.Errorf("monitor.strategy_health: %w", err)
		}
	}

	// 单独跑一次漂移检测，好把细节（类型 / 严重度 / 统计量）报出来 ——
	// monitor 的告警里只带一个 "drift_detected" 布尔。
	dailyReturns := make([]float64, len(points))
	for i, p := range points {
		dailyReturns[i] = p.DailyReturn
	}
	findings, drifted := detectDrift(dailyReturns, monitor.DefaultWindowSize)

	state, err := m.GetState(name)
	if err != nil {
		return nil, fmt.Errorf("monitor.strategy_health: %w", err)
	}
	alerts := m.CheckStatus()

	res := &strategyHealthResult{
		Strategy:        name,
		Points:          len(points),
		RollingSharpe:   state.RollingSharpe,
		RollingMaxDD:    state.RollingMaxDD,
		ConsecutiveLoss: state.ConsecutiveLosses,
		Drift:           findings,
		DriftDetected:   drifted,
		Alerts:          make([]alertView, 0, len(alerts)),
		Thresholds: thresholdView{
			MinSharpe:            thresholds.MinSharpe,
			MaxDrawdown:          thresholds.MaxDrawdown,
			ConsecutiveLossLimit: thresholds.ConsecutiveLossLimit,
			WindowSize:           monitor.DefaultWindowSize,
		},
	}
	if state.TotalTrades > 0 {
		res.WinRate = float64(state.Wins) / float64(state.TotalTrades)
	}
	for _, a := range alerts {
		res.Alerts = append(res.Alerts, alertView{
			Type:      string(a.Type),
			Severity:  string(a.Severity),
			Message:   a.Message,
			Value:     a.Value,
			Threshold: a.Threshold,
		})
	}
	res.Verdict = verdictFor(res, len(points))
	return res, nil
}

// detectDrift 跑漂移检测并把结果摊平。
//
// 样本不够时 drift.Detector 会报错，那不是"检测到漂移"也不是"没漂移"，
// 而是"判断不了" —— 如实返回，别当成健康。
func detectDrift(values []float64, window int) ([]driftFinding, bool) {
	det := drift.NewDetector(window, driftSignificance)
	raw, err := det.DetectAll(values)
	if err != nil {
		return nil, false
	}
	out := make([]driftFinding, 0, len(raw))
	any := false
	for _, r := range raw {
		if r == nil {
			continue
		}
		out = append(out, driftFinding{
			Type:      r.DriftType,
			Severity:  r.Severity,
			Message:   r.Message,
			Statistic: r.Statistic,
		})
		if r.DriftDetected {
			any = true
		}
	}
	return out, any
}

// verdictFor 给一句话结论。
//
// 样本不足时必须说出来 —— 用 10 个点算出来的"健康"比不判断更危险，
// 它会让人以为策略没问题。
func verdictFor(res *strategyHealthResult, points int) string {
	if points < monitor.DefaultWindowSize*2 {
		return fmt.Sprintf("样本不足（%d 点，漂移检测需要 %d+）：指标仅供参考，漂移未检测。",
			points, monitor.DefaultWindowSize*2)
	}
	switch {
	case res.DriftDetected && len(res.Alerts) > 0:
		return "漂移 + 指标破线：这个策略可能已经失效，不要按回测结果加仓。"
	case res.DriftDetected:
		return "检测到概念漂移，但指标尚未破线：收益分布变了，先减仓观察。"
	case len(res.Alerts) > 0:
		return "指标破线但未见漂移：可能是正常回撤，按告警类型处置。"
	default:
		return "未见漂移，指标在阈值内。"
	}
}

// toFloat 把 JSON 数字转成 float64（JSON 解出来可能是 int / float64 / string）。
func toFloat(v interface{}) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}

var _ = time.Now
