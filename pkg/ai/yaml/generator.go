package yaml

import (
	"fmt"
	"strings"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	"github.com/ruoxizhnya/quant-trading/pkg/fees"
)

// Generator generates YAML configuration from strategy intents
type Generator struct {
	// indentSize controls the number of spaces per indentation level
	indentSize int
}

// NewGenerator creates a new YAML generator
func NewGenerator() *Generator {
	return &Generator{indentSize: 2}
}

// NewGeneratorWithIndent creates a new YAML generator with custom indentation
func NewGeneratorWithIndent(indent int) *Generator {
	return &Generator{indentSize: indent}
}

// Config represents a complete strategy configuration in YAML
type Config struct {
	Strategy     StrategyConfig     `yaml:"strategy"`
	Backtest     BacktestConfig     `yaml:"backtest"`
	Data         DataConfig         `yaml:"data"`
	Risk         RiskConfig         `yaml:"risk,omitempty"`
	Execution    ExecutionConfig    `yaml:"execution,omitempty"`
	Optimization OptimizationConfig `yaml:"optimization,omitempty"`
	// Expression carries the ExpressionStrategy-specific config. When
	// present (non-zero), pkg/ai/yaml.LoadStrategy builds an
	// ExpressionStrategy from it. This is distinct from the top-level
	// Risk field: top-level Risk is engine-level stop-loss/take-profit;
	// Expression.Risk is post-signal weight risk control.
	// Added in S7-P3-2.
	Expression ExpressionYAML `yaml:"expression,omitempty"`
}

// ExpressionYAML maps 1:1 to expression.ExpressionStrategyConfig. It is
// the YAML representation of an ExpressionStrategy's signal/sizing/risk
// configuration. See pkg/strategy/expression/strategy.go.
type ExpressionYAML struct {
	Signal SignalYAML `yaml:"signal,omitempty"`
	Sizing SizingYAML `yaml:"sizing,omitempty"`
	Risk   RiskYAML   `yaml:"risk,omitempty"`
}

// SignalYAML maps to expression.SignalConfig.
type SignalYAML struct {
	Expression  string  `yaml:"expression"`
	Action      string  `yaml:"action,omitempty"`
	Direction   string  `yaml:"direction,omitempty"`
	MinStrength float64 `yaml:"min_strength,omitempty"`
	Lookback    int     `yaml:"lookback,omitempty"`
}

// SizingYAML maps to expression.SizingConfig.
type SizingYAML struct {
	Method      string  `yaml:"method,omitempty"`
	FixedWeight float64 `yaml:"fixed_weight,omitempty"`
	MaxPerStock float64 `yaml:"max_per_stock,omitempty"`
	MaxTotal    float64 `yaml:"max_total,omitempty"`
}

// RiskYAML maps to expression.RiskConfig. NOTE: this is NOT the same as
// the top-level RiskConfig (engine-level stop-loss). This controls the
// ExpressionStrategy's post-signal weight checks.
type RiskYAML struct {
	MaxPositionPct   float64 `yaml:"max_position_pct,omitempty"`
	MaxDrawdown      float64 `yaml:"max_drawdown,omitempty"`
	MaxOpenPositions int     `yaml:"max_open_positions,omitempty"`
	MinCashBuffer    float64 `yaml:"min_cash_buffer,omitempty"`
}

// StrategyConfig holds strategy-specific configuration
type StrategyConfig struct {
	Name        string                 `yaml:"name"`
	Type        string                 `yaml:"type"`
	Description string                 `yaml:"description"`
	Indicators  []string               `yaml:"indicators,omitempty"`
	Parameters  map[string]interface{} `yaml:"parameters"`
}

// BacktestConfig holds backtest settings
type BacktestConfig struct {
	StartDate      string  `yaml:"start_date"`
	EndDate        string  `yaml:"end_date"`
	InitialCapital float64 `yaml:"initial_capital"`
	CommissionRate float64 `yaml:"commission_rate"`
	SlippageRate   float64 `yaml:"slippage_rate"`
	RebalanceFreq  string  `yaml:"rebalance_frequency"`
}

// DataConfig holds data source configuration
type DataConfig struct {
	Universe    string   `yaml:"universe"`
	Timeframe   string   `yaml:"timeframe"`
	Providers   []string `yaml:"providers,omitempty"`
	AdjustPrice bool     `yaml:"adjust_price"`
}

// RiskConfig holds risk management settings
type RiskConfig struct {
	MaxPositions   int     `yaml:"max_positions,omitempty"`
	MaxDrawdown    float64 `yaml:"max_drawdown,omitempty"`
	StopLoss       float64 `yaml:"stop_loss,omitempty"`
	TakeProfit     float64 `yaml:"take_profit,omitempty"`
	PositionSizing string  `yaml:"position_sizing,omitempty"`
}

// ExecutionConfig holds execution settings
type ExecutionConfig struct {
	OrderType      string  `yaml:"order_type"`
	PriceTolerance float64 `yaml:"price_tolerance,omitempty"`
}

// OptimizationConfig holds parameter optimization settings
type OptimizationConfig struct {
	Enabled    bool     `yaml:"enabled"`
	Method     string   `yaml:"method,omitempty"`
	MaxIter    int      `yaml:"max_iterations,omitempty"`
	Parameters []string `yaml:"parameters,omitempty"`
}

// Generate creates a YAML configuration from an intent
func (g *Generator) Generate(i *intent.Intent) string {
	if i == nil {
		return ""
	}

	config := g.intentToConfig(i)
	return g.configToYAML(config)
}

// GenerateWithOptions creates a YAML configuration with additional options
func (g *Generator) GenerateWithOptions(i *intent.Intent, opts GenerateOptions) string {
	if i == nil {
		return ""
	}

	config := g.intentToConfig(i)

	// Override with options
	if opts.StartDate != "" {
		config.Backtest.StartDate = opts.StartDate
	}
	if opts.EndDate != "" {
		config.Backtest.EndDate = opts.EndDate
	}
	if opts.InitialCapital > 0 {
		config.Backtest.InitialCapital = opts.InitialCapital
	}
	if opts.RebalanceFreq != "" {
		config.Backtest.RebalanceFreq = opts.RebalanceFreq
	}

	return g.configToYAML(config)
}

// GenerateOptions allows customizing the generated YAML
type GenerateOptions struct {
	StartDate      string
	EndDate        string
	InitialCapital float64
	RebalanceFreq  string
}

// intentToConfig converts an Intent to a Config struct
func (g *Generator) intentToConfig(i *intent.Intent) Config {
	// Build parameters map
	params := make(map[string]interface{})
	for _, p := range i.Parameters {
		params[p.Name] = p.Value
	}

	config := Config{
		Strategy: StrategyConfig{
			Name:        i.StrategyName,
			Type:        string(i.StrategyType),
			Description: i.Description,
			Indicators:  i.Indicators,
			Parameters:  params,
		},
		Backtest: BacktestConfig{
			StartDate:      "2020-01-01",
			EndDate:        "2024-01-01",
			InitialCapital: 1000000,
			CommissionRate: fees.DefaultCommissionRate, // S7-P1-4: was 0.00025 (stale, diverged from fees.DefaultCommissionRate=0.0003)
			SlippageRate:   fees.DefaultSlippageRate,   // S7-P1-4: was 0.001 (FixedSlippageRate); YAML slippage_rate is the backtest assumption, must use DefaultSlippageRate (0.0001)
			RebalanceFreq:  "daily",
		},
		Data: DataConfig{
			Universe:    i.Universe,
			Timeframe:   i.Timeframe,
			Providers:   []string{"postgres", "tushare"},
			AdjustPrice: true,
		},
		Execution: ExecutionConfig{
			OrderType:      "market",
			PriceTolerance: 0.01,
		},
		Optimization: OptimizationConfig{
			Enabled: false,
			Method:  "grid_search",
			MaxIter: 100,
		},
	}

	// Add risk constraints if present
	if i.RiskConstraints != nil {
		rc := i.RiskConstraints
		if rc.MaxPositions != nil {
			config.Risk.MaxPositions = *rc.MaxPositions
		}
		if rc.MaxDrawdown != nil {
			config.Risk.MaxDrawdown = *rc.MaxDrawdown
		}
		if rc.StopLoss != nil {
			config.Risk.StopLoss = *rc.StopLoss
		}
		if rc.TakeProfit != nil {
			config.Risk.TakeProfit = *rc.TakeProfit
		}
		if rc.PositionSizing != nil {
			config.Risk.PositionSizing = *rc.PositionSizing
		}
	}

	// S7-P3-2: If the intent carries expression-strategy parameters
	// (signal_expr, sizing_method, etc.), populate the Expression
	// section so the generated YAML can be loaded directly by
	// LoadStrategy without LLM codegen.
	if expr, ok := intentToExpressionConfig(i); ok {
		config.Expression = expr
	}

	return config
}

// intentToExpressionConfig extracts ExpressionStrategy-related parameters
// from an intent. Returns (expr, true) if the intent has a signal_expr
// parameter (the trigger for emitting an expression: section); returns
// (zero, false) otherwise.
//
// The mapping mirrors ExpressionStrategy.Parameters() in
// pkg/strategy/expression/strategy.go. Only signal_expr is required to
// trigger emission; other fields fall back to their zero values and
// LoadStrategy/NewExpressionStrategy apply defaults downstream.
// defaultSignalExpression 是「意图类型 → 默认信号表达式」的确定性映射。
//
// P0-5：这层映射此前完全缺失。Generator 只在 intent 自带 signal_expr
// 参数时才产出 expression 段，而规则解析出来的 intent 只有一个语义标签
// （momentum / breakout / ...），于是生成的 YAML 根本加载不成策略 ——
// LoadStrategy 只认 expression 类型。整条实验链路就断在这里。
//
// 为什么做成确定性映射而不是让 LLM 每次现编：
//  1. 映射写在生成的 YAML 里，人能审阅、能直接改参数；
//  2. 同样的话术永远得到同样的起点，实验可复现；
//  3. AI 调的是旋钮（窗口、阈值、权重），不是重新发明一个策略 ——
//     这符合 ADR-023：AI 是操作仪器的实验员，不是造仪器的生成器。
//
// lookup 窗口只用于价量类意图；value / quality 用的是财报数据（季度频），
// 不看回看窗口。
//
// ADR-024 落地时这条注释写着「value / quality 映射不了，返回 ok=false」。
// P2-12 把 PE / PB / ROE / ROA 接进了表达式引擎，这两类现在能给出诚实的
// 表达式了 —— 但前提仍然是数据到位：没有财报时表达式会明确报错，绝不会
// 静默退化成用价格糊弄。
// defaultLookback 是意图没指定窗口时用的默认值（沿用此前写死的值）。
const defaultLookback = 20

func defaultSignalExpression(i *intent.Intent) (string, bool) {
	lb := intentLookback(i, defaultLookback)
	switch i.StrategyType {
	case intent.StrategyTypeMomentum:
		// lb 日涨幅的横截面排名，取最高的 20%。
		return fmt.Sprintf("cs_rank(ts_pct_change(close, %d)) > 0.8", lb), true
	case intent.StrategyTypeMeanReversion:
		// 相对 lb 日均值跌得越深分越高（反向做多）。
		return fmt.Sprintf("cs_rank(ts_mean(close, %d) - close) > 0.8", lb), true
	case intent.StrategyTypeTrendFollowing:
		// 短期均线上穿长期均线的幅度；长窗口沿用原先 3 倍的比例。
		return fmt.Sprintf("cs_rank(ts_mean(close, %d) - ts_mean(close, %d)) > 0.8", lb, lb*3), true
	case intent.StrategyTypeBreakout:
		// 收盘价相对 lb 日最高价的位置，越贴近/越突破分越高。
		return fmt.Sprintf("cs_rank(close - ts_max(high, %d)) > 0.8", lb), true
	case intent.StrategyTypeMultiFactor:
		// 动量 + 低波，两个横截面排名各占一半，取最高的 20%。
		return fmt.Sprintf("cs_rank(ts_pct_change(close, %d)) + cs_rank(neg(ts_std(close, %d))) > 1.6", lb, lb), true
	case intent.StrategyTypeValue:
		// 低 PE + 低 PB：两个「越便宜分越高」的排名各占一半。
		//
		// 前提是表达式引擎能拿到 pe / pb（P2-12）。拿不到时这个表达式会
		// **明确报错**，不会静默产出答非所问的回测数字 —— 这正是当初宁可
		// 返回 ok=false 也不给默认值的原因。
		//
		// PE/PB 为负（亏损 / 净资产为负）的数据在 OHLCVDataProvider 里就
		// 被折成 NaN，cs_rank 会把它们排除掉，所以「负值 = 最便宜」这个
		// 经典价值陷阱在这里不成立。
		return "cs_rank(neg(pe)) + cs_rank(neg(pb)) > 1.6", true
	case intent.StrategyTypeQuality:
		// 高 ROE + 高 ROA。与 value 不同，盈利率为负是**有意义**的差，
		// 原样参与排名（会自然排到末位），不做 NaN 处理。
		return "cs_rank(roe) + cs_rank(roa) > 1.6", true
	default:
		// custom：给不出诚实的表达式，交给调用方报错。
		return "", false
	}
}

// intentLookback 取出意图里的回看窗口。
//
// 非法值（<=0 或非数值）一律退回默认：搜索空间的边界偶尔会采到奇葩数，
// 不能让它污染出 ts_mean(close, -5) 这种表达式。
func intentLookback(i *intent.Intent, fallback int) int {
	if i == nil {
		return fallback
	}
	for _, p := range i.Parameters {
		if p.Name != "lookback_days" {
			continue
		}
		switch v := p.Value.(type) {
		case int:
			if v > 0 {
				return v
			}
		case int64:
			if v > 0 {
				return int(v)
			}
		case float64:
			if v > 0 {
				return int(v)
			}
		}
	}
	return fallback
}

func intentToExpressionConfig(i *intent.Intent) (ExpressionYAML, bool) {
	params := make(map[string]interface{}, len(i.Parameters))
	for _, p := range i.Parameters {
		params[p.Name] = p.Value
	}

	// 显式指定的 signal_expr 优先；没有就用意图类型的确定性默认（P0-5）。
	exprStr := ""
	if signalExpr, ok := params["signal_expr"]; ok {
		exprStr, _ = signalExpr.(string)
	}
	if exprStr == "" {
		def, ok := defaultSignalExpression(i)
		if !ok {
			return ExpressionYAML{}, false
		}
		exprStr = def
	}

	expr := ExpressionYAML{
		Signal: SignalYAML{
			Expression: exprStr,
		},
	}
	if v, ok := params["action"].(string); ok {
		expr.Signal.Action = v
	}
	if v, ok := params["direction"].(string); ok {
		expr.Signal.Direction = v
	}
	if v, ok := toFloat64(params["min_strength"]); ok {
		expr.Signal.MinStrength = v
	}
	if v, ok := toInt(params["lookback"]); ok {
		expr.Signal.Lookback = v
	}
	if v, ok := params["sizing_method"].(string); ok {
		expr.Sizing.Method = v
	}
	if v, ok := toFloat64(params["fixed_weight"]); ok {
		expr.Sizing.FixedWeight = v
	}
	if v, ok := toFloat64(params["max_per_stock"]); ok {
		expr.Sizing.MaxPerStock = v
	}
	if v, ok := toFloat64(params["max_total"]); ok {
		expr.Sizing.MaxTotal = v
	}
	if v, ok := toFloat64(params["max_position_pct"]); ok {
		expr.Risk.MaxPositionPct = v
	}
	if v, ok := toInt(params["max_open_positions"]); ok {
		expr.Risk.MaxOpenPositions = v
	}
	if v, ok := toFloat64(params["min_cash_buffer"]); ok {
		expr.Risk.MinCashBuffer = v
	}
	return expr, true
}

// toFloat64 extracts a float64 from an interface{} that may be float64
// or int (JSON numbers decode to float64; intent literals may be int).
func toFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	}
	return 0, false
}

// toInt extracts an int from an interface{} that may be int or float64.
func toInt(v interface{}) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case float64:
		return int(val), true
	}
	return 0, false
}

// quoteYAMLString wraps a string in double quotes and escapes inner
// double-quotes and backslashes so the result is a valid YAML
// double-quoted scalar. This is used for the expression string, which
// may contain characters (>, :, #) that would otherwise be
// misinterpreted by YAML parsers.
//
// Example: cs_rank(close) > 0.8 → "cs_rank(close) > 0.8"
// Example: ratio(a, "b") → "ratio(a, \"b\")"
func quoteYAMLString(s string) string {
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// configToYAML converts a Config to YAML string
func (g *Generator) configToYAML(config Config) string {
	var b strings.Builder
	indent := strings.Repeat(" ", g.indentSize)

	// Strategy section
	b.WriteString("strategy:\n")
	b.WriteString(fmt.Sprintf("%sname: %s\n", indent, config.Strategy.Name))
	b.WriteString(fmt.Sprintf("%stype: %s\n", indent, config.Strategy.Type))
	b.WriteString(fmt.Sprintf("%sdescription: %s\n", indent, config.Strategy.Description))

	if len(config.Strategy.Indicators) > 0 {
		b.WriteString(fmt.Sprintf("%sindicators:\n", indent))
		for _, ind := range config.Strategy.Indicators {
			b.WriteString(fmt.Sprintf("%s%s- %s\n", indent, indent, ind))
		}
	}

	b.WriteString(fmt.Sprintf("%sparameters:\n", indent))
	for name, value := range config.Strategy.Parameters {
		b.WriteString(fmt.Sprintf("%s%s%s: %v\n", indent, indent, name, value))
	}

	// Backtest section
	b.WriteString("\nbacktest:\n")
	b.WriteString(fmt.Sprintf("%sstart_date: %s\n", indent, config.Backtest.StartDate))
	b.WriteString(fmt.Sprintf("%send_date: %s\n", indent, config.Backtest.EndDate))
	b.WriteString(fmt.Sprintf("%sinitial_capital: %.0f\n", indent, config.Backtest.InitialCapital))
	b.WriteString(fmt.Sprintf("%scommission_rate: %.5f\n", indent, config.Backtest.CommissionRate))
	// S7-P1-4 (D5): %.5f, not %.3f — %.3f truncates 0.0001 to "0.000".
	b.WriteString(fmt.Sprintf("%sslippage_rate: %.5f\n", indent, config.Backtest.SlippageRate))
	b.WriteString(fmt.Sprintf("%srebalance_frequency: %s\n", indent, config.Backtest.RebalanceFreq))

	// Data section
	b.WriteString("\ndata:\n")
	b.WriteString(fmt.Sprintf("%suniverse: %s\n", indent, config.Data.Universe))
	b.WriteString(fmt.Sprintf("%stimeframe: %s\n", indent, config.Data.Timeframe))
	if len(config.Data.Providers) > 0 {
		b.WriteString(fmt.Sprintf("%sproviders:\n", indent))
		for _, provider := range config.Data.Providers {
			b.WriteString(fmt.Sprintf("%s%s- %s\n", indent, indent, provider))
		}
	}
	b.WriteString(fmt.Sprintf("%sadjust_price: %t\n", indent, config.Data.AdjustPrice))

	// Expression section (S7-P3-2): emitted only when the Expression
	// field is populated (i.e. the intent carried expression params).
	// String values are double-quoted to handle special characters
	// (colons, >, etc.) that would otherwise break YAML parsing.
	if config.Expression.Signal.Expression != "" {
		b.WriteString("\nexpression:\n")
		// signal:
		b.WriteString(fmt.Sprintf("%ssignal:\n", indent))
		b.WriteString(fmt.Sprintf("%s%sexpression: %s\n", indent, indent, quoteYAMLString(config.Expression.Signal.Expression)))
		if config.Expression.Signal.Action != "" {
			b.WriteString(fmt.Sprintf("%s%saction: %s\n", indent, indent, config.Expression.Signal.Action))
		}
		if config.Expression.Signal.Direction != "" {
			b.WriteString(fmt.Sprintf("%s%sdirection: %s\n", indent, indent, config.Expression.Signal.Direction))
		}
		if config.Expression.Signal.MinStrength != 0 {
			b.WriteString(fmt.Sprintf("%s%smin_strength: %g\n", indent, indent, config.Expression.Signal.MinStrength))
		}
		if config.Expression.Signal.Lookback > 0 {
			b.WriteString(fmt.Sprintf("%s%slookback: %d\n", indent, indent, config.Expression.Signal.Lookback))
		}
		// sizing:
		if config.Expression.Sizing.Method != "" || config.Expression.Sizing.FixedWeight > 0 ||
			config.Expression.Sizing.MaxPerStock > 0 || config.Expression.Sizing.MaxTotal > 0 {
			b.WriteString(fmt.Sprintf("%ssizing:\n", indent))
			if config.Expression.Sizing.Method != "" {
				b.WriteString(fmt.Sprintf("%s%smethod: %s\n", indent, indent, config.Expression.Sizing.Method))
			}
			if config.Expression.Sizing.FixedWeight > 0 {
				b.WriteString(fmt.Sprintf("%s%sfixed_weight: %g\n", indent, indent, config.Expression.Sizing.FixedWeight))
			}
			if config.Expression.Sizing.MaxPerStock > 0 {
				b.WriteString(fmt.Sprintf("%s%smax_per_stock: %g\n", indent, indent, config.Expression.Sizing.MaxPerStock))
			}
			if config.Expression.Sizing.MaxTotal > 0 {
				b.WriteString(fmt.Sprintf("%s%smax_total: %g\n", indent, indent, config.Expression.Sizing.MaxTotal))
			}
		}
		// risk:
		if config.Expression.Risk.MaxPositionPct > 0 || config.Expression.Risk.MaxOpenPositions > 0 ||
			config.Expression.Risk.MinCashBuffer > 0 {
			b.WriteString(fmt.Sprintf("%srisk:\n", indent))
			if config.Expression.Risk.MaxPositionPct > 0 {
				b.WriteString(fmt.Sprintf("%s%smax_position_pct: %g\n", indent, indent, config.Expression.Risk.MaxPositionPct))
			}
			if config.Expression.Risk.MaxOpenPositions > 0 {
				b.WriteString(fmt.Sprintf("%s%smax_open_positions: %d\n", indent, indent, config.Expression.Risk.MaxOpenPositions))
			}
			if config.Expression.Risk.MinCashBuffer > 0 {
				b.WriteString(fmt.Sprintf("%s%smin_cash_buffer: %g\n", indent, indent, config.Expression.Risk.MinCashBuffer))
			}
		}
	}

	// Risk section
	hasRisk := config.Risk.MaxPositions > 0 || config.Risk.MaxDrawdown > 0 ||
		config.Risk.StopLoss > 0 || config.Risk.TakeProfit > 0 ||
		config.Risk.PositionSizing != ""

	if hasRisk {
		b.WriteString("\nrisk:\n")
		if config.Risk.MaxPositions > 0 {
			b.WriteString(fmt.Sprintf("%smax_positions: %d\n", indent, config.Risk.MaxPositions))
		}
		if config.Risk.MaxDrawdown > 0 {
			b.WriteString(fmt.Sprintf("%smax_drawdown: %.2f\n", indent, config.Risk.MaxDrawdown))
		}
		if config.Risk.StopLoss > 0 {
			b.WriteString(fmt.Sprintf("%sstop_loss: %.2f\n", indent, config.Risk.StopLoss))
		}
		if config.Risk.TakeProfit > 0 {
			b.WriteString(fmt.Sprintf("%stake_profit: %.2f\n", indent, config.Risk.TakeProfit))
		}
		if config.Risk.PositionSizing != "" {
			b.WriteString(fmt.Sprintf("%sposition_sizing: %s\n", indent, config.Risk.PositionSizing))
		}
	}

	// Execution section
	b.WriteString("\nexecution:\n")
	b.WriteString(fmt.Sprintf("%sorder_type: %s\n", indent, config.Execution.OrderType))
	if config.Execution.PriceTolerance > 0 {
		b.WriteString(fmt.Sprintf("%sprice_tolerance: %.2f\n", indent, config.Execution.PriceTolerance))
	}

	// Optimization section
	b.WriteString("\noptimization:\n")
	b.WriteString(fmt.Sprintf("%senabled: %t\n", indent, config.Optimization.Enabled))
	if config.Optimization.Enabled {
		if config.Optimization.Method != "" {
			b.WriteString(fmt.Sprintf("%smethod: %s\n", indent, config.Optimization.Method))
		}
		if config.Optimization.MaxIter > 0 {
			b.WriteString(fmt.Sprintf("%smax_iterations: %d\n", indent, config.Optimization.MaxIter))
		}
		if len(config.Optimization.Parameters) > 0 {
			b.WriteString(fmt.Sprintf("%sparameters:\n", indent))
			for _, param := range config.Optimization.Parameters {
				b.WriteString(fmt.Sprintf("%s%s- %s\n", indent, indent, param))
			}
		}
	}

	return b.String()
}

// Validate checks if a YAML config string is valid
func (g *Generator) Validate(yamlStr string) error {
	if yamlStr == "" {
		return fmt.Errorf("YAML string is empty")
	}

	requiredSections := []string{"strategy:", "backtest:", "data:"}
	for _, section := range requiredSections {
		if !strings.Contains(yamlStr, section) {
			return fmt.Errorf("missing required section: %s", section)
		}
	}

	requiredStrategyFields := []string{"name:", "type:", "description:"}
	for _, field := range requiredStrategyFields {
		if !strings.Contains(yamlStr, field) {
			return fmt.Errorf("missing required strategy field: %s", field)
		}
	}

	return nil
}

// GenerateMinimal creates a minimal YAML configuration for quick testing
func (g *Generator) GenerateMinimal(i *intent.Intent) string {
	if i == nil {
		return ""
	}

	var b strings.Builder
	indent := strings.Repeat(" ", g.indentSize)

	b.WriteString("strategy:\n")
	b.WriteString(fmt.Sprintf("%sname: %s\n", indent, i.StrategyName))
	b.WriteString(fmt.Sprintf("%stype: %s\n", indent, i.StrategyType))
	b.WriteString(fmt.Sprintf("%sdescription: %s\n", indent, i.Description))

	if len(i.Parameters) > 0 {
		b.WriteString(fmt.Sprintf("%sparameters:\n", indent))
		for _, p := range i.Parameters {
			b.WriteString(fmt.Sprintf("%s%s%s: %v\n", indent, indent, p.Name, p.Value))
		}
	}

	b.WriteString("\nbacktest:\n")
	b.WriteString(fmt.Sprintf("%sstart_date: 2022-01-01\n", indent))
	b.WriteString(fmt.Sprintf("%send_date: 2024-01-01\n", indent))
	b.WriteString(fmt.Sprintf("%sinitial_capital: 1000000\n", indent))

	b.WriteString("\ndata:\n")
	b.WriteString(fmt.Sprintf("%suniverse: %s\n", indent, i.Universe))
	b.WriteString(fmt.Sprintf("%stimeframe: %s\n", indent, i.Timeframe))

	return b.String()
}

// MergeConfigs merges multiple YAML configurations into a single composite config
func (g *Generator) MergeConfigs(configs []string) (string, error) {
	if len(configs) == 0 {
		return "", fmt.Errorf("no configs to merge")
	}
	if len(configs) == 1 {
		return configs[0], nil
	}

	var b strings.Builder
	b.WriteString("# Composite Strategy Configuration\n")
	b.WriteString(fmt.Sprintf("# Merged from %d strategies\n\n", len(configs)))

	b.WriteString("composite:\n")
	indent := strings.Repeat(" ", g.indentSize)
	b.WriteString(fmt.Sprintf("%sname: merged_strategy\n", indent))
	b.WriteString(fmt.Sprintf("%stype: composite\n", indent))
	b.WriteString(fmt.Sprintf("%sdescription: Auto-generated composite strategy\n", indent))
	b.WriteString(fmt.Sprintf("%sstrategies:\n", indent))

	for i, config := range configs {
		b.WriteString(fmt.Sprintf("%s%s# Strategy %d\n", indent, indent, i+1))
		lines := strings.Split(config, "\n")
		for _, line := range lines {
			if line != "" {
				b.WriteString(fmt.Sprintf("%s%s%s\n", indent, indent, line))
			}
		}
	}

	return b.String(), nil
}
