package main

// buildToolsRegistry 的装配级测试（S7-P3-4 / Hermes Phase 1.7）——
// 随 ai-service 迁入（原 cmd/analysis/setup_test.go 的对应段，逐字保留）。

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ──────────────────────────────────────────────────────────────────────
// buildToolsRegistry tests (S7-P3-4 / Hermes Phase 1.7)
// ──────────────────────────────────────────────────────────────────────

// stubWFRunner satisfies builtin.WalkForwardRunner for registry
// construction tests. Execute is never called — we only verify tool
// registration, not execution.
type stubWFRunner struct{}

func (stubWFRunner) RunWalkForward(_ context.Context, _ string, _ []string, _, _ string, _ domain.WalkForwardParams) (*domain.WalkForwardReport, error) {
	return &domain.WalkForwardReport{}, nil
}

// stubRegimeDetector satisfies builtin.RegimeDetectorClient for registry
// construction tests. DetectRegime is never called — we only verify tool
// registration, not execution.
type stubRegimeDetector struct{}

func (stubRegimeDetector) DetectRegime(_ context.Context, _ []domain.OHLCV) (*domain.MarketRegime, error) {
	return &domain.MarketRegime{}, nil
}

// stubResearchProfile satisfies builtin.ResearchProfileClient for registry
// construction tests. GetResearchProfile is never called — we only verify
// tool registration, not execution.
type stubResearchProfile struct{}

func (stubResearchProfile) GetResearchProfile(_ context.Context, _ string) (*storage.ResearchProfile, error) {
	return nil, nil
}

// TestBuildToolsRegistry_RegistersAll19Tools verifies that
// buildToolsRegistry registers all 19 tools (8 original S7-P3-3 tools +
// 8 Hermes Phase 1 tools + 1 Hermes Phase 2.2 tool + 1 Hermes Phase 2.3 tool
// + 1 EQD-P2-1 tool) with the correct names. This is the wiring-level test —
// individual tool behavior is covered in pkg/ai/tools/builtin/*_test.go.
//
// Reuses stubBacktestRunner from handlers_pipeline_test.go (same package).
func TestBuildToolsRegistry_RegistersAll19Tools(t *testing.T) {

	// Minimal viper config — only the keys buildToolsRegistry reads.
	v := newTestViper(t)

	// Gene pools with nil internal pgxpool — construction is safe, only
	// Execute would panic (which we never call in this test).
	factorPool := gene_pool.NewFactorPool(nil)
	strategyPool := gene_pool.NewStrategyPool(nil)

	reg := BuildToolsRegistry(v, "http://test-analysis:8085", &stubBacktestRunner{}, stubWFRunner{}, factorPool, strategyPool, stubRegimeDetector{}, stubResearchProfile{}, nil, zerolog.Nop())
	require.NotNil(t, reg)

	tools := reg.List()
	assert.Len(t, tools, 21, "registry should contain exactly 21 tools (8 original + 8 Hermes Phase 1 + 1 Phase 2.2 + 1 Phase 2.3 + 1 EQD-P2-1 + 1 P2-3 + 1 P2-6)")

	// Collect names into a set for O(1) lookup.
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}

	// Verify all 19 expected tool names are present.
	expectedTools := []string{
		// ── Original 8 (S7-P3-3) ──
		"backtest.run",
		"factor.compute",
		"factor.evaluate",
		"data.ohlcv",
		"data.stocks",
		"data.fundamentals",
		"strategy.list",
		"strategy.get",
		// ── Hermes Phase 1 additions (8) ──
		"validate_factor",       // Phase 1.1 (L1 gate)
		"compute_factor_ic",     // Phase 1.2 (L2 gate)
		"walk_forward_validate", // Phase 1.3 (L4 gate)
		"list_factors",          // Phase 1.4 (gene pool)
		"save_factor",           // Phase 1.4 (gene pool)
		"list_strategies",       // Phase 1.5 (gene pool)
		"save_strategy",         // Phase 1.5 (gene pool)
		"summarize_backtest",    // Phase 1.6
		// ── Hermes Phase 2.2 additions (1) ──
		"get_strategy_lineage", // Phase 2.2 (gene pool lineage)
		// ─ Hermes Phase 2.3 additions (1) ──
		"get_market_regime", // Phase 2.3 (market regime detection)
		// ─ EQD-P2-1 addition (1) ──
		"research.profile", // EQD-P2-1 (bridge B2, EquityDeep research archive)
	}
	for _, name := range expectedTools {
		assert.True(t, names[name], "tool %q should be registered", name)
	}
}

// TestBuildToolsRegistry_NoDuplicateNames ensures there are no accidental
// name collisions in the registry (each tool name must be unique).
func TestBuildToolsRegistry_NoDuplicateNames(t *testing.T) {
	v := newTestViper(t)
	factorPool := gene_pool.NewFactorPool(nil)
	strategyPool := gene_pool.NewStrategyPool(nil)

	reg := BuildToolsRegistry(v, "http://test-analysis:8085", &stubBacktestRunner{}, stubWFRunner{}, factorPool, strategyPool, stubRegimeDetector{}, stubResearchProfile{}, nil, zerolog.Nop())
	require.NotNil(t, reg)

	tools := reg.List()
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if seen[tool.Name] {
			t.Errorf("duplicate tool name: %q", tool.Name)
		}
		seen[tool.Name] = true
	}
}

// newTestViper creates a viper config with the minimal keys needed by
// buildToolsRegistry (server.port, data_service.url).
func newTestViper(t *testing.T) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.Set("server.port", 9999)
	v.Set("data_service.url", "http://test-data:8081")
	return v
}

