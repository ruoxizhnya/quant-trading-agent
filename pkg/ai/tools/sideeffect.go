package tools

// SideEffect classifies what a Tool does to the world outside the
// process. It exists so the HTTP layer can pick a role requirement
// without hardcoding a tool-name list of its own — the classification
// lives next to the Tools it describes.
//
// AUD-02 (ODR-065 H5): before this, every Tool was executable by any
// authenticated user (and, in open-access mode, by anyone). Several
// Tools have real write side effects — save_factor / save_strategy
// mutate the gene pool — so a viewer-tier token could create
// strategies that the AI orchestrator would later pick up.
type SideEffect int

const (
	// SideEffectRead is a pure query: no state change, no external
	// mutation. Callable by any authenticated role, including viewer.
	SideEffectRead SideEffect = iota

	// SideEffectWrite mutates durable state (gene pool rows, strategy
	// registry) or spends material resources (full backtest runs).
	// Requires trader or admin.
	SideEffectWrite

	// SideEffectAdmin performs a privileged operation that a normal
	// trading workflow does not need.
	SideEffectAdmin
)

// String renders the SideEffect for logs and JSON.
func (s SideEffect) String() string {
	switch s {
	case SideEffectRead:
		return "read"
	case SideEffectWrite:
		return "write"
	case SideEffectAdmin:
		return "admin"
	default:
		return "unknown"
	}
}

// sideEffectRegistry is the audited classification of every Tool
// registered in cmd/analysis/setup.go.
//
// WHY A TABLE AND NOT A FIELD ON ToolCore: adding a method to ToolCore
// would break every existing Tool implementation at compile time and
// force the classification into the tool structs, where it would be
// easy to set inconsistently. A single table is one place to review.
//
// FAIL-CLOSED CONTRACT: Classify returns SideEffectAdmin for any name
// NOT in this table. A newly registered Tool is therefore admin-only
// until someone deliberately audits it and adds a row. That is the
// intended direction of failure — a forgotten row locks things down
// rather than opening them up.
//
// KEEP IN SYNC: tools_sideeffect_test.go asserts that this table
// covers exactly the set of Tools the composition root registers. If
// you register a new Tool without adding a row here, that test fails.
var sideEffectRegistry = map[string]SideEffect{
	// ── Read: pure queries ────────────────────────────────────────────
	"backtest.run":            SideEffectRead, // computes, persists nothing
	"data.fundamentals":       SideEffectRead,
	"data.ohlcv":              SideEffectRead,
	"data.stocks":             SideEffectRead,
	"factor.compute":          SideEffectRead,
	"factor.evaluate":         SideEffectRead,
	"factor.hypothesis":       SideEffectRead,
	"compute_factor_ic":       SideEffectRead,
	"validate_factor":         SideEffectRead,
	"list_factors":            SideEffectRead,
	"list_strategies":         SideEffectRead,
	"strategy.get":            SideEffectRead,
	"strategy.list":           SideEffectRead,
	"get_strategy_lineage":    SideEffectRead,
	"get_market_regime":       SideEffectRead,
	"monitor.strategy_health": SideEffectRead,
	"summarize_backtest":      SideEffectRead,
	"walk_forward_validate":   SideEffectRead,
	"research.profile":        SideEffectRead,

	// ── Write: mutates the gene pool ──────────────────────────────────
	// save_factor / save_strategy insert rows that the AI orchestrator
	// then treats as candidate strategies. A viewer must not be able to
	// seed the pool.
	"save_factor":   SideEffectWrite,
	"save_strategy": SideEffectWrite,
}

// Classify returns the audited SideEffect of the named Tool.
//
// Unregistered names return SideEffectAdmin (fail-closed) — see the
// sideEffectRegistry doc comment.
func Classify(name string) SideEffect {
	if se, ok := sideEffectRegistry[name]; ok {
		return se
	}
	return SideEffectAdmin
}

// ClassifiedNames returns every name present in the classification
// table. Used by the drift test to compare against the registry.
func ClassifiedNames() []string {
	out := make([]string, 0, len(sideEffectRegistry))
	for n := range sideEffectRegistry {
		out = append(out, n)
	}
	return out
}
