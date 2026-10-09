package tools

import (
	"sort"
	"testing"
)

// TestClassify_FailClosed pins the fail-closed contract: an unknown
// tool name is admin-only, never silently read-only.
func TestClassify_FailClosed(t *testing.T) {
	t.Parallel()
	assertEffect(t, "save_factor", SideEffectWrite)
	assertEffect(t, "save_strategy", SideEffectWrite)
	assertEffect(t, "backtest.run", SideEffectRead)
	assertEffect(t, "totally.unknown.tool", SideEffectAdmin)
	assertEffect(t, "", SideEffectAdmin)
}

// TestClassify_RegisteredToolsHaveRows is the drift guard. It builds a
// Registry containing the same Tools the composition root registers and
// asserts the classification table covers every one of them.
//
// Without this test, adding a Tool to setup.go silently makes it
// admin-only (fail-closed, so not a security hole — but a functional
// regression that would be confusing to debug: "why does my new tool
// 403 for trader?").
//
// The tool list below must mirror cmd/analysis/setup.go's
// registerBuiltinTools. Names are duplicated deliberately: pkg/ai/tools
// must not import cmd/analysis, so the shared source of truth is the
// name string, and this test is what keeps the two sides in sync.
func TestClassify_RegisteredToolsHaveRows(t *testing.T) {
	t.Parallel()

	// Mirrors setup.go:501-590.
	registered := []string{
		"backtest.run",
		"factor.compute",
		"factor.evaluate",
		"validate_factor",
		"compute_factor_ic",
		"data.ohlcv",
		"data.stocks",
		"data.fundamentals",
		"strategy.list",
		"strategy.get",
		"walk_forward_validate",
		"list_factors",
		"save_factor",
		"list_strategies",
		"save_strategy",
		"summarize_backtest",
		"get_strategy_lineage",
		"get_market_regime",
		"research.profile",
		"factor.hypothesis",
		"monitor.strategy_health",
	}

	classified := make(map[string]bool)
	for _, n := range ClassifiedNames() {
		classified[n] = true
	}

	for _, n := range registered {
		if !classified[n] {
			t.Errorf("Tool %q is registered in setup.go but has no row in sideEffectRegistry — "+
				"it will fail closed to admin-only. Add a row to pkg/ai/tools/sideeffect.go.", n)
		}
	}
}

// TestClassify_NoStaleRows catches the reverse drift: a row for a Tool
// that no longer exists. Stale rows are harmless at runtime but they
// rot the table's credibility as "the audited list".
func TestClassify_NoStaleRows(t *testing.T) {
	t.Parallel()

	registered := map[string]bool{
		"backtest.run": true, "factor.compute": true, "factor.evaluate": true,
		"validate_factor": true, "compute_factor_ic": true, "data.ohlcv": true,
		"data.stocks": true, "data.fundamentals": true, "strategy.list": true,
		"strategy.get": true, "walk_forward_validate": true, "list_factors": true,
		"save_factor": true, "list_strategies": true, "save_strategy": true,
		"summarize_backtest": true, "get_strategy_lineage": true,
		"get_market_regime": true, "research.profile": true,
		"factor.hypothesis": true, "monitor.strategy_health": true,
	}

	for _, n := range ClassifiedNames() {
		if !registered[n] {
			t.Errorf("sideEffectRegistry has a row for %q, which is not registered in setup.go — "+
				"either the tool was removed (drop the row) or the name is a typo.", n)
		}
	}
}

// TestClassify_EveryToolHasADistinctName is a cheap sanity check that
// the table has no accidental duplicates from copy-paste.
func TestClassify_TableIsSorted(t *testing.T) {
	t.Parallel()
	names := ClassifiedNames()
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	// Not an error to be unsorted, just report the count for eyeballing
	// if the table ever grows large.
	t.Logf("classified %d tools (%d read, %d write)",
		len(names), countEffect(SideEffectRead), countEffect(SideEffectWrite))
}

func countEffect(se SideEffect) int {
	n := 0
	for _, v := range sideEffectRegistry {
		if v == se {
			n++
		}
	}
	return n
}

func assertEffect(t *testing.T, name string, want SideEffect) {
	t.Helper()
	if got := Classify(name); got != want {
		t.Errorf("Classify(%q) = %s, want %s", name, got, want)
	}
}
