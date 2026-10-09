package agents

import (
	"context"
	"fmt"
	"math"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/client"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/metrics"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/search"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/validator"
	"github.com/ruoxizhnya/quant-trading/pkg/expression"
	"github.com/ruoxizhnya/quant-trading/pkg/statistics"
)

// ValidationLevel defines the depth of validation.
type ValidationLevel int

const (
	// L1Syntax validates formula syntax only.
	L1Syntax ValidationLevel = 1
	// L2QuickEval validates with quick evaluation on sample data.
	L2QuickEval ValidationLevel = 2
	// L3StandardBacktest runs full backtest.
	L3StandardBacktest ValidationLevel = 3
	// L4WalkForward runs walk-forward validation.
	L4WalkForward ValidationLevel = 4
)

// ValidationResult holds the result of factor validation.
type ValidationResult struct {
	Level       ValidationLevel `json:"level"`
	Passed      bool            `json:"passed"`
	Score       float64         `json:"score"`
	IC          float64         `json:"ic"`
	IR          float64         `json:"ir"`
	Turnover    float64         `json:"turnover"`
	Sharpe      float64         `json:"sharpe"`
	Errors      []string        `json:"errors,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
	Formula     string          `json:"formula"`
	Inputs      []string        `json:"inputs"`
	Description string          `json:"description"`

	// P1-12 L4 walk-forward fields. Populated only when level >= L4
	// and a BacktestRunner is configured. Empty otherwise.
	ISSharpe    float64 `json:"is_sharpe,omitempty"`
	OOSSharpe   float64 `json:"oos_sharpe,omitempty"`
	SharpeGap   float64 `json:"sharpe_gap,omitempty"`
	Robustness  float64 `json:"robustness,omitempty"`
	OverfitRisk string  `json:"overfit_risk,omitempty"`
	WindowCount int     `json:"window_count,omitempty"`
}

// ValidateAgent validates factor hypotheses through multiple levels.
type ValidateAgent struct {
	parser        *expression.Parser
	evaluator     *expression.Evaluator
	icCalc        *metrics.ICCalculator
	turnoverCalc  *metrics.TurnoverCalculator
	dataProvider  expression.DataProvider
	btClient      *client.BacktestClient
	codeValidator *validator.CodeValidator

	// defaultStockPool is the universe used by validateL3 when running
	// a quick backtest. S7-P0-3 (ODR-043-3): previously hardcoded to
	// US tickers (see ODR-043-3 for the exact symbols) which cannot
	// resolve in the A-share backtest engine. Now defaults to a small
	// set of liquid A-share large-caps and can be overridden via
	// SetStockPool.
	defaultStockPool []string

	// P1-12 (ODR-013 Sprint 6): L4 walk-forward dependencies.
	// btRunner is the pluggable backtest surface. nil disables L4
	// (validateL4 falls back to the legacy "requires walk-forward
	// integration" warning). Set via SetBacktestRunner.
	btRunner BacktestRunner
	// walkForward is the underlying search.WalkForwardValidator.
	// nil → runWalkForward uses a fresh default (no shared state).
	walkForward *search.WalkForwardValidator
	// l4Cfg is the active L4 window / gap config. nil → package
	// defaults from validate_l4.go. Set via SetL4Config.
	l4Cfg *L4Config
}

// defaultAShareStockPool is the default L3 validation universe: a small
// set of liquid A-share large-caps spanning banking, real estate, and
// consumer sectors. These symbols resolve in the project's A-share
// backtest engine (PostgreSQL stocks table) and give a quick but
// meaningful Sharpe signal during factor validation.
//
// S7-P0-3 (ODR-043-3): replaces the prior US-ticker default (see
// ODR-043-3 for the exact symbols) which produced misleading L3
// failures because those symbols do not exist in the A-share data store.
var defaultAShareStockPool = []string{
	"000001.SZ", // 平安银行 (Ping An Bank)
	"000002.SZ", // 万科A (Vanke)
	"600000.SH", // 浦发银行 (SPDB)
	"600519.SH", // 贵州茅台 (Kweichow Moutai)
}

// NewValidateAgent creates a new ValidateAgent.
func NewValidateAgent(dataProvider expression.DataProvider) *ValidateAgent {
	return &ValidateAgent{
		parser:           expression.NewParser(),
		icCalc:           metrics.NewICCalculator(),
		turnoverCalc:     metrics.NewTurnoverCalculator(),
		dataProvider:     dataProvider,
		codeValidator:    validator.NewCodeValidator(),
		defaultStockPool: append([]string(nil), defaultAShareStockPool...),
	}
}

// NewValidateAgentWithBacktest creates a ValidateAgent with backtest client.
// P1-12: also auto-wires a BacktestRunner from the client so L4 walk-forward
// works out of the box when the backtest service is reachable.
func NewValidateAgentWithBacktest(dataProvider expression.DataProvider, btClient *client.BacktestClient) *ValidateAgent {
	a := &ValidateAgent{
		parser:           expression.NewParser(),
		icCalc:           metrics.NewICCalculator(),
		turnoverCalc:     metrics.NewTurnoverCalculator(),
		dataProvider:     dataProvider,
		btClient:         btClient,
		codeValidator:    validator.NewCodeValidator(),
		btRunner:         NewHTTPBacktestRunner(btClient),
		walkForward:      search.NewWalkForwardValidator(),
		defaultStockPool: append([]string(nil), defaultAShareStockPool...),
	}
	return a
}

// SetBacktestRunner overrides the BacktestRunner. Pass nil to disable
// L4 (validateL4 falls back to the legacy warning).
func (a *ValidateAgent) SetBacktestRunner(r BacktestRunner) {
	a.btRunner = r
}

// SetWalkForwardValidator overrides the WalkForwardValidator. Pass
// nil to use a fresh default per call.
func (a *ValidateAgent) SetWalkForwardValidator(w *search.WalkForwardValidator) {
	a.walkForward = w
}

// SetL4Config sets the active L4 window / gap config. Pass nil to
// reset to the package defaults.
func (a *ValidateAgent) SetL4Config(cfg *L4Config) {
	a.l4Cfg = cfg
}

// SetStockPool overrides the default L3 backtest stock pool. Pass nil
// or an empty slice to reset to the A-share default
// (defaultAShareStockPool). This is useful when a caller wants to
// validate a factor against a different universe (e.g. CSI 500
// constituents) without modifying the package default.
//
// S7-P0-3 (ODR-043-3): the pool was previously a hardcoded literal
// inside validateL3 and could not be overridden.
func (a *ValidateAgent) SetStockPool(pool []string) {
	if len(pool) == 0 {
		a.defaultStockPool = append([]string(nil), defaultAShareStockPool...)
		return
	}
	a.defaultStockPool = append([]string(nil), pool...)
}

// l4Config returns the active L4Config, applying package defaults
// to a value copy if no config was set.
func (a *ValidateAgent) l4Config() L4Config {
	if a.l4Cfg == nil {
		return applyL4Defaults(L4Config{})
	}
	return applyL4Defaults(*a.l4Cfg)
}

// Validate runs validation at the specified level.
func (a *ValidateAgent) Validate(ctx context.Context, formula string, level ValidationLevel) (*ValidationResult, error) {
	result := &ValidationResult{
		Level:    level,
		Formula:  formula,
		Errors:   []string{},
		Warnings: []string{},
	}

	// L1: Syntax validation
	if err := a.validateL1(result); err != nil {
		return result, nil // Return with errors, don't fail
	}

	if level >= L2QuickEval {
		if err := a.validateL2(ctx, result); err != nil {
			return result, nil
		}
	}

	if level >= L3StandardBacktest {
		if err := a.validateL3(ctx, result); err != nil {
			return result, nil
		}
	}

	if level >= L4WalkForward {
		if err := a.validateL4(ctx, result); err != nil {
			return result, nil
		}
	}

	result.Passed = len(result.Errors) == 0 && result.Score > 0
	return result, nil
}

// ValidateGene validates a FactorGene and updates its metrics.
func (a *ValidateAgent) ValidateGene(ctx context.Context, gene *gene_pool.FactorGene, level ValidationLevel) (*ValidationResult, error) {
	result, err := a.Validate(ctx, gene.Formula, level)
	if err != nil {
		return nil, err
	}

	// Update gene with validation results
	gene.IC = result.IC
	gene.IR = result.IR
	gene.Turnover = result.Turnover
	gene.Sharpe = result.Sharpe
	gene.Fitness = result.Score

	if result.Passed {
		gene.Status = "validated"
	} else {
		gene.Status = "rejected"
	}

	return result, nil
}

// validateL1 performs syntax validation.
func (a *ValidateAgent) validateL1(result *ValidationResult) error {
	expr, err := a.parser.Parse(result.Formula)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Parse error: %v", err))
		return err
	}

	result.Inputs = expr.Inputs

	if err := expr.Validate(); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Validation error: %v", err))
		return err
	}

	// Check for common issues
	if len(expr.Inputs) == 0 {
		result.Warnings = append(result.Warnings, "Formula has no data inputs (pure literal)")
	}

	// Check formula complexity
	complexity := estimateComplexity(expr.AST)
	if complexity > 10 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("High complexity: %d nodes", complexity))
	}

	result.Score = 1.0 // Base score for valid syntax
	return nil
}

// validateL2 performs quick evaluation validation.
func (a *ValidateAgent) validateL2(ctx context.Context, result *ValidationResult) error {
	if a.dataProvider == nil {
		result.Warnings = append(result.Warnings, "No data provider available for L2 validation")
		return fmt.Errorf("no data provider")
	}

	a.evaluator = expression.NewEvaluator(a.dataProvider)

	expr, err := a.parser.Parse(result.Formula)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Parse error: %v", err))
		return err
	}

	// Evaluate on sample data
	evalResult, err := a.evaluator.Evaluate(expr.AST, 0)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Evaluation error: %v", err))
		return err
	}

	// Check for NaN/Inf in results
	nanCount := 0
	totalCount := 0
	for _, values := range evalResult {
		for _, v := range values {
			totalCount++
			if math.IsNaN(v) || math.IsInf(v, 0) {
				nanCount++
			}
		}
	}

	if totalCount > 0 {
		nanRatio := float64(nanCount) / float64(totalCount)
		if nanRatio > 0.5 {
			result.Errors = append(result.Errors, fmt.Sprintf("Too many NaN/Inf values: %.1f%%", nanRatio*100))
		} else if nanRatio > 0.1 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Elevated NaN/Inf values: %.1f%%", nanRatio*100))
		}
	}

	// Check result variance
	variance := computeVariance(evalResult)
	if variance == 0 {
		result.Errors = append(result.Errors, "Zero variance in factor values")
	} else if variance < 1e-10 {
		result.Warnings = append(result.Warnings, "Very low variance in factor values")
	}

	result.Score = 2.0 // Base score for successful evaluation
	return nil
}

// validateL3 performs standard backtest validation.
//
// P1-12 (ODR-013 Sprint 6): when no btClient is configured we still
// must reach validateL4 — the L4 walk-forward is independently driven
// by the BacktestRunner. We keep the legacy warning for diagnostics
// but do NOT propagate an error (returning nil lets Validate()
// proceed to validateL4).
func (a *ValidateAgent) validateL3(ctx context.Context, result *ValidationResult) error {
	if a.btClient == nil {
		result.Warnings = append(result.Warnings, "No backtest client available for L3 validation")
		result.Score = 3.0
		return nil
	}

	// Run quick backtest via API.
	// S7-P0-3 (ODR-043-3): use the configurable A-share stock pool
	// (defaultStockPool) instead of the former hardcoded US tickers
	// which could not resolve in the A-share backtest engine.
	req := client.BacktestRequest{
		StrategyName: result.Formula,
		StockPool:    a.defaultStockPool,
		StartDate:    "2023-01-01",
		EndDate:      "2024-01-01",
	}

	btResult, err := a.btClient.RunBacktest(ctx, req)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Backtest failed: %v", err))
		result.Score = 3.0
		return err
	}

	if btResult != nil {
		result.Sharpe = btResult.SharpeRatio
		result.Score = 3.0 + btResult.SharpeRatio
	}

	return nil
}

// validateL4 performs walk-forward validation.
//
// P1-12 (ODR-013 Sprint 6): replaced the previous stub with an actual
// walk-forward loop. The fail-gate is the SharpeGap: when
// (ISSharpe - OOSSharpe) / |ISSharpe| exceeds L4Config.SharpeGapLimit
// (default 0.30), the result is marked as failed with a clear error.
//
// Backward compatibility: when no BacktestRunner is configured
// (agent built via NewValidateAgent without SetBacktestRunner), the
// legacy warning is preserved so existing call-sites that pass nil
// to L4 still get a non-fatal result.
func (a *ValidateAgent) validateL4(ctx context.Context, result *ValidationResult) error {
	if a.btRunner == nil {
		result.Warnings = append(result.Warnings,
			"L4 validation requires BacktestRunner (use NewValidateAgentWithBacktest or SetBacktestRunner)")
		result.Score = 4.0
		return nil
	}

	cfg := a.l4Config()
	outcome, err := a.runWalkForward(ctx, result.Formula, cfg)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("L4 walk-forward failed: %v", err))
		result.Score = 4.0
		return err
	}

	// Populate the L4 fields on the result for downstream consumers
	// (UI dashboard, gene pool, pipeline orchestrator).
	result.ISSharpe = outcome.ISSharpe
	result.OOSSharpe = outcome.OOSSharpe
	result.SharpeGap = outcome.SharpeGap
	result.Robustness = outcome.Robustness
	result.OverfitRisk = outcome.OverfitRisk
	result.WindowCount = len(outcome.Windows)

	// Score: base 4.0 (passed L4) plus the better of (OOS, IS) but
	// capped so a high IS Sharpe can't mask a high gap.
	sharpe := outcome.OOSSharpe
	if outcome.ISSharpe < outcome.OOSSharpe {
		sharpe = outcome.ISSharpe
	}
	result.Score = 4.0 + math.Max(0, sharpe)

	// P1-12 fail-gate: Sharpe degradation > configured limit.
	if outcome.SharpeGap > outcome.Limit {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"P1-12 fail-gate: IS/OOS Sharpe gap %.2f%% exceeds limit %.2f%% (IS=%.3f, OOS=%.3f, risk=%s)",
			outcome.SharpeGap*100, outcome.Limit*100, outcome.ISSharpe, outcome.OOSSharpe, outcome.OverfitRisk,
		))
		result.Passed = false
		// Keep the warnings array for non-fatal diagnostics
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"Walk-forward robustness %.2f across %d window(s)",
			outcome.Robustness, len(outcome.Windows),
		))
		return nil
	}

	result.Passed = len(result.Errors) == 0 && result.Score > 0
	return nil
}

// estimateComplexity counts AST nodes.
func estimateComplexity(node expression.Node) int {
	if node == nil {
		return 0
	}

	count := 1
	for _, child := range node.Children() {
		count += estimateComplexity(child)
	}
	return count
}

// computeVariance calculates the sample (n-1) variance of all finite
// evaluation results across runs.
//
// The NaN/Inf filter is preserved here (legacy behaviour) and the
// remaining math is delegated to pkg/statistics (ODR-013 P1-21).
func computeVariance(results map[string][]float64) float64 {
	var allValues []float64
	for _, values := range results {
		for _, v := range values {
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				allValues = append(allValues, v)
			}
		}
	}
	return statistics.SampleVariance(allValues)
}

// ComputeFitness calculates a composite fitness score.
func (a *ValidateAgent) ComputeFitness(ic, ir, turnover, sharpe float64) float64 {
	// Composite fitness: higher IC, higher IR, lower turnover, higher Sharpe
	// Normalize each component
	icScore := math.Max(0, ic) * 100              // IC typically 0.01-0.1
	irScore := math.Max(0, ir) * 50               // IR typically 0.1-1.0
	turnoverScore := math.Max(0, 1-turnover) * 20 // Lower turnover is better
	sharpeScore := math.Max(0, sharpe) * 30       // Sharpe typically 0.5-2.0

	fitness := icScore + irScore + turnoverScore + sharpeScore
	return math.Min(fitness, 100.0) // Cap at 100
}

// BatchValidate validates multiple genes at once.
func (a *ValidateAgent) BatchValidate(ctx context.Context, genes []*gene_pool.FactorGene, level ValidationLevel) ([]*ValidationResult, error) {
	var results []*ValidationResult

	for _, gene := range genes {
		result, err := a.ValidateGene(ctx, gene, level)
		if err != nil {
			// Continue with other genes even if one fails
			result = &ValidationResult{
				Level:   level,
				Passed:  false,
				Formula: gene.Formula,
				Errors:  []string{fmt.Sprintf("Validation error: %v", err)},
			}
		}
		results = append(results, result)
	}

	return results, nil
}
