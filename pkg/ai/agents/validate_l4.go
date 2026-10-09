package agents

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/client"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/search"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// P1-12 (ODR-013 Sprint 6): L4 walk-forward validation.
//
// The original validateL4 (validate.go:255-261) was a stub that warned
// "L4 validation requires walk-forward integration" — a real Phase 4 P0
// fail-gate. This file replaces the stub with an actual walk-forward
// loop driven by the search.WalkForwardValidator and a pluggable
// BacktestRunner (default: HTTP BacktestClient).
//
// Fail-gate contract (Sprint 6 P1-12 acceptance):
//   - For at least one walk-forward window:
//       trainMetrics := runner.RunBacktest(formula, ISRange)
//       testMetrics  := runner.RunBacktest(formula, OOSRange)
//   - SharpeGap := (ISSharpe - OOSSharpe) / max(ISSharpe, epsilon)
//   - If SharpeGap > l4Cfg.SharpeGapLimit  →  ValidationResult.Passed = false
//   - OverfitRisk = "low" | "medium" | "high" (search.assessOverfitRisk)
//
// The fail-gate is intentionally conservative (default gap limit 0.30)
// so a strategy with suspicious in-sample / out-of-sample divergence
// is rejected before being promoted to Phase 4's next stage.

// Default L4 walk-forward date window. Matches validateL3's
// 2023-01-01..2024-01-01 OOS slice and adds a 2-year IS slice so
// the gap has a meaningful sample size.
const (
	l4DefaultISStart  = "2021-01-01"
	l4DefaultISOOSplt = "2023-01-01"
	l4DefaultOOSEnd   = "2024-01-01"
	l4DefaultGapLimit = 0.30
)

// L4Config configures the L4 walk-forward window, the gap-limit
// fail-gate, and whether to run multiple step-forward windows or a
// single IS/OOS split.
//
// Zero values fall back to the package defaults (see consts above).
type L4Config struct {
	// ISStart is the inclusive start date of the in-sample window.
	ISStart string
	// ISOOSplit is the date that splits in-sample from out-of-sample.
	// IS range = [ISStart, ISOOSplit); OOS range = [ISOOSplit, OOSEnd].
	ISOOSplit string
	// OOSEnd is the inclusive end date of the out-of-sample window.
	OOSEnd string
	// StockPool is the symbol set to backtest. nil → small default.
	StockPool []string
	// SharpeGapLimit is the maximum allowed
	//   |ISSharpe - OOSSharpe| / |ISSharpe|
	// before L4 marks the factor as failed. 0 → 0.30.
	SharpeGapLimit float64
	// StepSizeDays, if > 0, enables multi-window walk-forward:
	// train on [ISStart, ISStart+trainDays] and test on the next
	// StepSizeDays, then slide forward by StepSizeDays and repeat.
	// 0 → single IS/OOS split.
	StepSizeDays int
	// MinTrainDays sets the validator's minTrainSize (default 252).
	// Only consulted when StepSizeDays == 0 (single window).
	MinTrainDays int
	// MinTestDays sets the validator's minTestSize (default 63).
	MinTestDays int
}

// BacktestRunner is the minimum surface validateL4 needs from a
// backtest engine. *client.BacktestClient satisfies this via
// NewHTTPBacktestRunner; tests inject mock runners.
type BacktestRunner interface {
	RunBacktest(ctx context.Context, formula string, stockPool []string, startDate, endDate string) (BacktestMetrics, error)
}

// BacktestMetrics is the L4-relevant subset of domain.BacktestResult.
// Kept narrow so mocks are trivial.
type BacktestMetrics struct {
	Sharpe float64
	Return float64
}

// httpBacktestRunner adapts *client.BacktestClient to BacktestRunner.
type httpBacktestRunner struct {
	c *client.BacktestClient
}

// NewHTTPBacktestRunner wraps a BacktestClient so it satisfies
// BacktestRunner. Pass nil for the in-process / no-network case.
func NewHTTPBacktestRunner(c *client.BacktestClient) BacktestRunner {
	if c == nil {
		return nil
	}
	return &httpBacktestRunner{c: c}
}

func (r *httpBacktestRunner) RunBacktest(ctx context.Context, formula string, stockPool []string, startDate, endDate string) (BacktestMetrics, error) {
	res, err := r.c.RunBacktest(ctx, client.BacktestRequest{
		StrategyName: formula,
		StockPool:    stockPool,
		StartDate:    startDate,
		EndDate:      endDate,
	})
	if err != nil {
		return BacktestMetrics{}, err
	}
	return toBacktestMetrics(res), nil
}

func toBacktestMetrics(r *domain.BacktestResult) BacktestMetrics {
	if r == nil {
		return BacktestMetrics{}
	}
	return BacktestMetrics{Sharpe: r.SharpeRatio, Return: r.TotalReturn}
}

// l4DefaultStockPool is used when L4Config.StockPool is nil.
// Kept small so the default walk-forward completes in seconds.
var l4DefaultStockPool = []string{"AAPL", "GOOGL", "MSFT"}

// applyL4Defaults fills zero-valued L4Config fields with the package
// defaults. Operates on a value copy so caller's config is untouched.
func applyL4Defaults(cfg L4Config) L4Config {
	if cfg.ISStart == "" {
		cfg.ISStart = l4DefaultISStart
	}
	if cfg.ISOOSplit == "" {
		cfg.ISOOSplit = l4DefaultISOOSplt
	}
	if cfg.OOSEnd == "" {
		cfg.OOSEnd = l4DefaultOOSEnd
	}
	if cfg.StockPool == nil {
		cfg.StockPool = l4DefaultStockPool
	}
	if cfg.SharpeGapLimit == 0 {
		cfg.SharpeGapLimit = l4DefaultGapLimit
	}
	if cfg.MinTrainDays == 0 {
		cfg.MinTrainDays = 252
	}
	if cfg.MinTestDays == 0 {
		cfg.MinTestDays = 63
	}
	return cfg
}

// runWalkForward is the P1-12 implementation: it dispatches to
// search.WalkForwardValidator.Validate (single window) or
// ValidateMultipleWindows (step-forward), translating the
// index-based train/test callbacks into date-range backtest calls
// via the injected BacktestRunner.
//
// Result fields populated on out (when err == nil):
//   - ISSharpe, OOSSharpe, SharpeGap, Robustness, OverfitRisk
func (a *ValidateAgent) runWalkForward(ctx context.Context, formula string, cfg L4Config) (out L4WalkForwardOutcome, err error) {
	cfg = applyL4Defaults(cfg)

	// Convert date ranges to integer day counts. WalkForwardValidator
	// is index-based; we map [0..totalDays] to [ISStart..OOSEnd].
	isStart, err := time.Parse("2006-01-02", cfg.ISStart)
	if err != nil {
		return out, fmt.Errorf("invalid ISStart %q: %w", cfg.ISStart, err)
	}
	split, err := time.Parse("2006-01-02", cfg.ISOOSplit)
	if err != nil {
		return out, fmt.Errorf("invalid ISOOSplit %q: %w", cfg.ISOOSplit, err)
	}
	oosEnd, err := time.Parse("2006-01-02", cfg.OOSEnd)
	if err != nil {
		return out, fmt.Errorf("invalid OOSEnd %q: %w", cfg.OOSEnd, err)
	}
	if !split.After(isStart) {
		return out, fmt.Errorf("ISOOSplit %s must be after ISStart %s", cfg.ISOOSplit, cfg.ISStart)
	}
	if !oosEnd.After(split) {
		return out, fmt.Errorf("OOSEnd %s must be after ISOOSplit %s", cfg.OOSEnd, cfg.ISOOSplit)
	}
	totalDays := int(oosEnd.Sub(isStart).Hours() / 24)
	isDays := int(split.Sub(isStart).Hours() / 24)

	// Build the train/test funcs that bridge indices → date strings
	// → BacktestRunner calls. search.WalkForwardValidator expects
	// Metrics-only callbacks (no error), so per-window failures are
	// captured in runErr and surfaced to the caller after Validate
	// returns. Returning zero Metrics on a failed window is benign:
	// the validator will still aggregate it, but we'll check runErr
	// and discard the result.
	runner := a.btRunner
	stockPool := cfg.StockPool
	dateAt := func(idx int) string {
		return isStart.AddDate(0, 0, idx).Format("2006-01-02")
	}
	var runErr error
	runBacktestOnce := func(startIdx, endIdx int) search.Metrics {
		if runErr != nil {
			return search.Metrics{}
		}
		startDate := dateAt(startIdx)
		endDate := dateAt(endIdx)
		bm, e := runner.RunBacktest(ctx, formula, stockPool, startDate, endDate)
		if e != nil {
			runErr = fmt.Errorf("backtest %s..%s failed: %w", startDate, endDate, e)
			return search.Metrics{}
		}
		return search.Metrics{TotalReturn: bm.Return, SharpeRatio: bm.Sharpe}
	}
	// isDays is informational (single-window path uses validator
	// defaults; kept for clarity / future overrides).
	_ = isDays

	wfv := a.walkForward
	if wfv == nil {
		wfv = search.NewWalkForwardValidator()
	}
	wfv = wfv.WithSizes(cfg.MinTrainDays, cfg.MinTestDays)

	// Dispatch: multi-window (step-forward) vs single split. Both
	// paths share the runBacktestOnce closure so the error sentinel
	// is uniform.
	var wfResult *search.WalkForwardResult
	if cfg.StepSizeDays > 0 {
		wfResult, err = wfv.ValidateMultipleWindows(totalDays, cfg.StepSizeDays, runBacktestOnce, runBacktestOnce)
	} else {
		wfResult, err = wfv.Validate(totalDays, runBacktestOnce, runBacktestOnce)
	}
	if err != nil {
		return out, err
	}
	if runErr != nil {
		// One of the per-window backtests failed.
		return out, runErr
	}

	isSharpe := wfResult.ISMetrics.SharpeRatio
	oosSharpe := wfResult.OOSMetrics.SharpeRatio

	// Gap = max(0, (IS - OOS)) / max(|IS|, epsilon).
	//
	// Rationale per quadrant:
	//   - IS > 0, OOS < IS: degradation → gap = (IS-OOS)/IS > 0
	//   - IS > 0, OOS >= IS: improvement → gap = 0
	//   - IS < 0, OOS > IS: improvement → gap = 0
	//   - IS < 0, OOS <= IS: further degradation → gap = (IS-OOS)/|IS| > 0
	//   - IS = 0: degenerate, gap = 0 (no in-sample signal to lose)
	//
	// Clamping the numerator at 0 means "OOS beats IS" is never
	// treated as a degradation, even when IS is negative.
	denom := math.Abs(isSharpe)
	if denom < 1e-9 {
		denom = 1e-9
	}
	numerator := isSharpe - oosSharpe
	if numerator < 0 {
		numerator = 0
	}
	gap := numerator / denom
	return L4WalkForwardOutcome{
		ISSharpe:    isSharpe,
		OOSSharpe:   oosSharpe,
		SharpeGap:   gap,
		Robustness:  wfResult.Robustness,
		OverfitRisk: wfResult.OverfitRisk,
		Windows:     wfResult.Windows,
		Limit:       cfg.SharpeGapLimit,
	}, nil
}

// L4WalkForwardOutcome is the structured result of runWalkForward.
// ValidationResult embeds the Sharpe / Risk / Gap fields and
// downstream consumers can read Windows for per-window breakdown.
type L4WalkForwardOutcome struct {
	ISSharpe    float64
	OOSSharpe   float64
	SharpeGap   float64
	Robustness  float64
	OverfitRisk string
	Windows     []search.WindowResult
	Limit       float64
}
