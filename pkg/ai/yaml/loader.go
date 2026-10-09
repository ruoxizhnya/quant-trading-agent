// Package yaml provides YAML configuration generation and parsing for
// AI-generated strategies. The Generator emits YAML from an intent; the
// Loader (this file) parses YAML back into structured config and can
// build an executable ExpressionStrategy from it.
//
// S7-P3-2 (ODR-043 Sprint 7): Added ParseConfig and LoadStrategy to
// enable the flow AI → YAML → ExpressionStrategy → backtest, bypassing
// the LLM Go-codegen + compile path for expression-based strategies.
package yaml

import (
	"fmt"

	yamlv3 "gopkg.in/yaml.v3"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy/expression"
)

// ParseConfig parses a YAML config string into a Config struct.
//
// This is the inverse of Generator.configToYAML: it accepts a YAML
// document matching the Config schema (strategy/backtest/data/risk/
// execution/optimization/expression) and returns the structured form.
// Required fields are validated: the strategy section and its name
// field must be present.
//
// Returns a clear error for:
//   - empty input
//   - malformed YAML (syntax errors)
//   - missing top-level strategy: section
//   - missing strategy.name field
//
// The Expression section is optional; if absent, the returned Config
// has a zero-valued Expression field. Callers that need an
// ExpressionStrategy should use LoadStrategy, which applies detection
// logic (expression section presence or strategy.type == "expression").
func ParseConfig(yamlStr string) (*Config, error) {
	if yamlStr == "" {
		return nil, fmt.Errorf("yaml: ParseConfig: input is empty")
	}

	var config Config
	if err := yamlv3.Unmarshal([]byte(yamlStr), &config); err != nil {
		return nil, fmt.Errorf("yaml: ParseConfig: %w", err)
	}

	// Validate required top-level section.
	if config.Strategy.Name == "" && config.Strategy.Type == "" && config.Strategy.Description == "" {
		return nil, fmt.Errorf("yaml: ParseConfig: missing required section: strategy:")
	}

	// Validate required strategy.name field. We treat name as the
	// primary identifier (it becomes the registry key downstream).
	if config.Strategy.Name == "" {
		return nil, fmt.Errorf("yaml: ParseConfig: missing required strategy field: name:")
	}

	return &config, nil
}

// expressionTemplateName is the name self-registered by pkg/strategy/expression's
// init() function. Loading a YAML with this name would collide with the
// pre-registered default, so we reject it and ask the caller to pick a
// distinct name.
const expressionTemplateName = "expression_template"

// LoadStrategy parses a YAML config and builds an executable
// expression.ExpressionStrategy from it.
//
// Detection logic (which kind of strategy to build):
//  1. If the 'expression:' section is present with a non-empty
//     signal.expression → build an ExpressionStrategy using the
//     section's values (defaults applied for omitted sub-fields).
//  2. Else if strategy.type == "expression" → build an ExpressionStrategy
//     using the package defaults (cs_rank(close) > 0.8, equal sizing,
//     10% per stock, 20 positions, 5% cash buffer).
//  3. Otherwise → return an error: LoadStrategy only supports
//     expression-type strategies.
//
// Validation:
//   - strategy.name must be non-empty (also enforced by ParseConfig)
//   - strategy.name must not be "expression_template" (collides with
//     the self-registered default)
//   - expression.signal.direction (if present) must be one of:
//     long, short, close, hold
//
// The returned strategy is NOT registered. Callers who want it in the
// global registry should use LoadAndRegister, or call
// strategy.GlobalRegister themselves. This keeps LoadStrategy a pure
// "YAML → object" function for easy testing.
func LoadStrategy(yamlStr string) (strategy.Strategy, error) {
	config, err := ParseConfig(yamlStr)
	if err != nil {
		return nil, err
	}

	name := config.Strategy.Name
	if name == expressionTemplateName {
		return nil, fmt.Errorf(
			"yaml: LoadStrategy: strategy.name %q is reserved by the self-registered "+
				"expression_template default; please choose a distinct name",
			name)
	}

	// Decide which config to build from.
	hasExpressionSection := config.Expression.Signal.Expression != ""
	if !hasExpressionSection && config.Strategy.Type != "expression" {
		return nil, fmt.Errorf(
			"yaml: LoadStrategy: only expression-type strategies are supported; "+
				"either add an 'expression:' section with signal.expression or set "+
				"strategy.type to \"expression\" (got type=%q)",
			config.Strategy.Type)
	}

	exprCfg := expression.ExpressionStrategyConfig{}
	if hasExpressionSection {
		exprCfg, err = buildExpressionConfig(config.Expression)
		if err != nil {
			return nil, fmt.Errorf("yaml: LoadStrategy: %w", err)
		}
	}
	// When hasExpressionSection is false but type == "expression", we
	// pass a zero ExpressionStrategyConfig; NewExpressionStrategy applies
	// the default signal config (cs_rank(close) > 0.8) and the sizer /
	// risk controllers apply their own defaults for zero sub-fields.

	s, err := expression.NewExpressionStrategy(name, exprCfg)
	if err != nil {
		return nil, fmt.Errorf("yaml: LoadStrategy: %w", err)
	}
	return s, nil
}

// LoadAndRegister parses YAML, builds an ExpressionStrategy, and
// registers it with the global strategy registry. This is a convenience
// wrapper around LoadStrategy + strategy.GlobalRegister for callers who
// want the strategy immediately available for backtest lookup by name.
//
// Returns the registered strategy. If a strategy with the same name is
// already registered, GlobalRegister returns an error which is passed
// through to the caller; use strategy.GlobalGet to check first, or use
// LoadStrategy + Configure for in-place reconfiguration.
func LoadAndRegister(yamlStr string) (strategy.Strategy, error) {
	s, err := LoadStrategy(yamlStr)
	if err != nil {
		return nil, err
	}
	if err := strategy.GlobalRegister(s); err != nil {
		return nil, fmt.Errorf("yaml: LoadAndRegister: %w", err)
	}
	return s, nil
}

// ExpressionParamsFromConfig builds the flat params map that
// expression.ExpressionStrategy.Configure expects, from a parsed
// Config's Expression section. Only non-zero values are included so
// partial updates preserve the existing config (Configure semantics:
// missing keys keep current values).
//
// Used by Pipeline.ExecuteFromYAML to reconfigure an already-registered
// strategy in place when the same name is loaded again. The param-name
// strings here mirror ExpressionStrategy.Parameters() in
// pkg/strategy/expression/strategy.go and intentToExpressionConfig in
// generator.go — keeping the mapping in one package avoids divergent
// copies.
//
// When the Expression section is empty (the strategy.type == "expression"
// path with no explicit expression: block), the returned map contains
// only signal_expr="" — Configure treats this as "keep current value".
func ExpressionParamsFromConfig(config *Config) map[string]interface{} {
	if config == nil {
		return nil
	}
	expr := config.Expression
	params := map[string]interface{}{
		"signal_expr": expr.Signal.Expression,
	}
	if expr.Signal.Action != "" {
		params["action"] = expr.Signal.Action
	}
	if expr.Signal.Direction != "" {
		params["direction"] = expr.Signal.Direction
	}
	if expr.Signal.MinStrength != 0 {
		params["min_strength"] = expr.Signal.MinStrength
	}
	if expr.Signal.Lookback != 0 {
		params["lookback"] = expr.Signal.Lookback
	}
	if expr.Sizing.Method != "" {
		params["sizing_method"] = expr.Sizing.Method
	}
	if expr.Sizing.FixedWeight != 0 {
		params["fixed_weight"] = expr.Sizing.FixedWeight
	}
	if expr.Sizing.MaxPerStock != 0 {
		params["max_per_stock"] = expr.Sizing.MaxPerStock
	}
	if expr.Sizing.MaxTotal != 0 {
		params["max_total"] = expr.Sizing.MaxTotal
	}
	if expr.Risk.MaxPositionPct != 0 {
		params["max_position_pct"] = expr.Risk.MaxPositionPct
	}
	if expr.Risk.MaxOpenPositions != 0 {
		params["max_open_positions"] = expr.Risk.MaxOpenPositions
	}
	if expr.Risk.MinCashBuffer != 0 {
		params["min_cash_buffer"] = expr.Risk.MinCashBuffer
	}
	return params
}

// buildExpressionConfig maps the YAML representation to the
// expression.ExpressionStrategyConfig struct, validating the direction
// string along the way.
func buildExpressionConfig(expr ExpressionYAML) (expression.ExpressionStrategyConfig, error) {
	signalCfg, err := buildSignalConfig(expr.Signal)
	if err != nil {
		return expression.ExpressionStrategyConfig{}, err
	}
	sizingCfg := buildSizingConfig(expr.Sizing)
	riskCfg := buildRiskConfig(expr.Risk)

	return expression.ExpressionStrategyConfig{
		SignalCfg: signalCfg,
		SizingCfg: sizingCfg,
		RiskCfg:   riskCfg,
	}, nil
}

// buildSignalConfig maps SignalYAML to expression.SignalConfig, validating
// the direction string.
func buildSignalConfig(s SignalYAML) (expression.SignalConfig, error) {
	dir := domain.DirectionLong // default
	if s.Direction != "" {
		dir = domain.Direction(s.Direction)
		if err := validateDirection(dir); err != nil {
			return expression.SignalConfig{}, err
		}
	}
	return expression.SignalConfig{
		Expression:  s.Expression,
		Action:      s.Action,
		Direction:   dir,
		MinStrength: s.MinStrength,
		Lookback:    s.Lookback,
	}, nil
}

// validateDirection returns an error if d is not one of the recognized
// domain.Direction constants.
func validateDirection(d domain.Direction) error {
	switch d {
	case domain.DirectionLong, domain.DirectionShort, domain.DirectionClose, domain.DirectionHold:
		return nil
	default:
		return fmt.Errorf("invalid direction %q: must be one of long, short, close, hold", string(d))
	}
}

// buildSizingConfig maps SizingYAML to expression.SizingConfig. Zero
// values are preserved (NewPositionSizer applies defaults).
func buildSizingConfig(s SizingYAML) expression.SizingConfig {
	return expression.SizingConfig{
		Method:      expression.SizingMethod(s.Method),
		FixedWeight: s.FixedWeight,
		MaxPerStock: s.MaxPerStock,
		MaxTotal:    s.MaxTotal,
	}
}

// buildRiskConfig maps RiskYAML to expression.RiskConfig. Zero values
// are preserved (NewRiskController applies defaults).
func buildRiskConfig(r RiskYAML) expression.RiskConfig {
	return expression.RiskConfig{
		MaxPositionPct:   r.MaxPositionPct,
		MaxDrawdown:      r.MaxDrawdown,
		MaxOpenPositions: r.MaxOpenPositions,
		MinCashBuffer:    r.MinCashBuffer,
	}
}
