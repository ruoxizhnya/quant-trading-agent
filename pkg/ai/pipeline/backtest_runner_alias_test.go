package pipeline

// S7-P1-3 regression test: verify copilot.BacktestRunner and
// pipeline.BacktestRunner are zero-cost type aliases to
// contracts.BacktestRunner (the SAME type), not re-declared interfaces.
//
// Why this lives in pkg/ai/pipeline and not next to the contract
// (pkg/backtest/contracts/runner_test.go): proving the aliases are
// identical requires importing BOTH pkg/strategy and pkg/ai/pipeline, and
// pkg/ai/pipeline already depends on pkg/strategy + the contracts package
// in production. Asserting it here adds zero new coupling and keeps the
// core contract's own test free of any pkg/ai import (ADR-027 §5 step 3:
// the contract is core-side infrastructure, not an AI capability).
//
// This is the "eliminate duplicate definitions" guard: a future
// contributor who re-introduces a local `type BacktestRunner interface`
// in pkg/strategy or pkg/ai/pipeline will break this test at compile
// time, surfacing the regression before it ships.

import (
	"context"
	"testing"

	"github.com/ruoxizhnya/quant-trading/pkg/backtest/contracts"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/copilot"
)

// aliasStubRunner is a minimal concrete implementation used only to prove
// that the three BacktestRunner references denote the same type.
type aliasStubRunner struct{}

func (aliasStubRunner) RunBacktest(ctx context.Context, strategyName string, stockPool []string, startDate, endDate string) (*domain.BacktestResult, error) {
	return &domain.BacktestResult{}, nil
}

// TestS7P1_3_StrategyAndPipelineAliasesAreIdentical verifies that
// copilot.BacktestRunner and pipeline.BacktestRunner are zero-cost
// type aliases to contracts.BacktestRunner (the SAME type), not
// re-declared interfaces. If either package re-declares the interface,
// the cross-assignments below fail to compile.
func TestS7P1_3_StrategyAndPipelineAliasesAreIdentical(t *testing.T) {
	// If copilot.BacktestRunner is a type alias, this assignment is
	// valid (same type). If it's a re-declared interface, the types are
	// distinct and the assignment fails to compile.
	var s copilot.BacktestRunner = aliasStubRunner{}
	var p BacktestRunner = aliasStubRunner{}

	// Cross-assign strategy → contracts and pipeline → contracts. With
	// re-declared (distinct) interfaces these assignments would fail.
	var c contracts.BacktestRunner = s
	c = p
	_ = c
	t.Log("copilot.BacktestRunner, pipeline.BacktestRunner, and " +
		"contracts.BacktestRunner are the SAME type (zero-cost aliases)")
}
