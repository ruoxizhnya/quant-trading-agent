package builtin

import (
	"context"
	"fmt"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
)

// ═══════════════════════════════════════════════════════════════════════
//  GetStrategyLineageTool (Hermes Phase 2.2)
// ═══════════════════════════════════════════════════════════════════════
//
// GetStrategyLineageTool recursively retrieves the ancestor tree of a
// strategy from the gene pool. The lineage is built by walking each
// strategy's ParentIDs upward: a strategy with parents [B, C] contributes
// two children to the result tree, and the same expansion is applied to B
// and C until either:
//   - a strategy has no parents (leaf), or
//   - max_depth is reached (lineage truncated at this depth), or
//   - a cycle is detected (parent ID already visited → recorded in
//     CyclesDetected, recursion stops for that branch), or
//   - a parent ID cannot be found in the pool (recorded in
//     MissingParentIDs, branch terminates).
//
// Why this tool exists: the autonomous factor-mining loop (design §8.1)
// needs a "reflect and decide" step where Hermes inspects the lineage of
// a candidate strategy to understand its mutation history — e.g. "this
// strategy was derived from sg_x by adding cs_neutralize, which was
// derived from sg_y by tightening max_per_stock". Without lineage the
// agent would re-discover variants it already explored.
//
// Tool name: "get_strategy_lineage"
// Input: strategy_id (required), max_depth (optional, default 5, max 10)
// Output: *strategyLineageResult — a tree of strategyLineageNode plus
// diagnostic lists (cycles detected, missing parents, depth reached).

const (
	// DefaultLineageMaxDepth is the recursion limit used when the caller
	// doesn't specify max_depth. 5 is enough to walk a typical 5-generation
	// mutation chain (e.g. origin → v1 → v2 → v3 → v4 → v5) without
	// unbounded work.
	DefaultLineageMaxDepth = 5
	// MaxLineageMaxDepth clamps an explicit max_depth argument. The gene
	// pool's tree is bounded in practice by the mutation operator's branch
	// factor, but an adversarial caller could pass max_depth=10000 — we
	// refuse to expand more than 10 levels to keep response size sane
	// (each level can multiply by the # of parents).
	MaxLineageMaxDepth = 10
)

// strategyLineageNode is one node in the lineage tree. It carries the
// strategy's key identifiers + metrics (enough for Hermes to decide
// whether to mutate/improve/abandon) plus the recursively-expanded
// Parents slice.
//
// Heavy fields (Code, Params, Description, timestamps) are intentionally
// omitted to keep the JSON payload LLM-friendly (design §4.3: summary
// < 500 tokens). The agent can call list_strategies or strategy.get with
// a specific ID if it needs the full record.
type strategyLineageNode struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	StrategyType string                 `json:"strategy_type"`
	FactorIDs    []string               `json:"factor_ids"`
	ParentIDs    []string               `json:"parent_ids"`
	Sharpe       float64                `json:"sharpe"`
	Fitness      float64                `json:"fitness"`
	Generation   int                    `json:"generation"`
	Status       string                 `json:"status"`
	Parents      []*strategyLineageNode `json:"parents,omitempty"`
}

// strategyLineageResult is the top-level return value of
// GetStrategyLineageTool.Execute.
//
//   - Root is the lineage tree starting from the requested strategy.
//   - DepthReached is the deepest level successfully expanded (0 = only
//     the root). May equal MaxDepth if the lineage was truncated.
//   - MaxDepth is the effective depth limit applied (after clamping).
//   - CyclesDetected lists parent IDs that pointed back to an
//     already-visited strategy (typically indicates data corruption
//     or a malformed mutation). Empty if none.
//   - MissingParentIDs lists parent IDs that could not be found in the
//     pool (typically indicates the parent was deleted, or never saved).
//     Empty if none.
type strategyLineageResult struct {
	Root             *strategyLineageNode `json:"root"`
	DepthReached     int                  `json:"depth_reached"`
	MaxDepth         int                  `json:"max_depth"`
	CyclesDetected   []string             `json:"cycles_detected,omitempty"`
	MissingParentIDs []string             `json:"missing_parent_ids,omitempty"`
}

// GetStrategyLineageTool retrieves a strategy's lineage tree from the
// gene pool. See the package-level doc comment above for the full
// algorithm description.
type GetStrategyLineageTool struct {
	pool StrategyPoolClient
}

var _ tools.Tool = (*GetStrategyLineageTool)(nil)

// NewGetStrategyLineageTool constructs a GetStrategyLineageTool backed
// by the given StrategyPoolClient. Panics on nil — fail-loud at wiring
// time (composition root), not at first Execute call.
func NewGetStrategyLineageTool(pool StrategyPoolClient) *GetStrategyLineageTool {
	if pool == nil {
		panic("builtin: NewGetStrategyLineageTool called with nil StrategyPoolClient")
	}
	return &GetStrategyLineageTool{pool: pool}
}

func (t *GetStrategyLineageTool) Name() string { return "get_strategy_lineage" }

func (t *GetStrategyLineageTool) Description() string {
	return "Recursively fetch the lineage (ancestor tree) of a strategy from the gene pool. " +
		"Use this to understand a strategy's mutation history (which parents it was derived from) " +
		"before mutating/improving it — avoids re-discovering variants you already explored. " +
		"Returns a nested tree of ancestors with key metrics, plus diagnostics for cycles " +
		"and missing parent IDs."
}

func (t *GetStrategyLineageTool) Parameters() []tools.Parameter {
	return []tools.Parameter{
		{
			Name:        "strategy_id",
			Type:        "string",
			Description: "ID of the strategy whose lineage to retrieve (e.g. 'sg_abc123').",
			Required:    true,
		},
		{
			Name:        "max_depth",
			Type:        "int",
			Description: "Maximum recursion depth. 0 = return only the root (no parents expanded). Default 5. Clamped to 10 if higher.",
			Required:    false,
			Default:     DefaultLineageMaxDepth,
		},
	}
}

func (t *GetStrategyLineageTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{
		Type:        "object",
		Description: "Strategy lineage tree: the root strategy plus recursively-expanded parents. Diagnostics list cycles and missing parent IDs encountered during traversal.",
		Fields: []tools.OutputField{
			{Name: "root", Type: "object", Description: "Root lineage node (the requested strategy). Each node has: id, name, strategy_type, factor_ids, parent_ids, sharpe, fitness, generation, status, parents (recursive)."},
			{Name: "depth_reached", Type: "int", Description: "Deepest level successfully expanded (0 = only the root)."},
			{Name: "max_depth", Type: "int", Description: "Effective depth limit applied after clamping."},
			{Name: "cycles_detected", Type: "[]string", Description: "Parent IDs that pointed back to an already-visited strategy (data corruption or malformed mutation)."},
			{Name: "missing_parent_ids", Type: "[]string", Description: "Parent IDs not found in the pool (parent deleted or never saved)."},
		},
	}
}

// Execute runs the lineage query. Errors are returned when:
//   - strategy_id is missing/empty/not a string (wrapped ErrInvalidArgs)
//   - max_depth has the wrong type (wrapped ErrInvalidArgs)
//   - the root strategy itself cannot be fetched (any Get error —
//     typically pgx.ErrNoRows wrapped as "get strategy gene: ...").
//
// Missing parents *within* the tree are NOT errors — they are recorded
// in result.MissingParentIDs and the recursion continues.
func (t *GetStrategyLineageTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	rootID, err := requireString(args, "strategy_id")
	if err != nil {
		return nil, err
	}

	maxDepth, err := optionalInt(args, "max_depth", DefaultLineageMaxDepth)
	if err != nil {
		return nil, err
	}
	// Clamp: negative → 0; above ceiling → ceiling.
	if maxDepth < 0 {
		maxDepth = 0
	}
	if maxDepth > MaxLineageMaxDepth {
		maxDepth = MaxLineageMaxDepth
	}

	result, err := t.buildLineage(ctx, rootID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("get_strategy_lineage: %w", err)
	}
	return result, nil
}

// buildLineage performs the recursive ancestor traversal. It is
// extracted from Execute so it can be unit-tested directly without
// constructing an args map.
//
// The traversal uses the standard DFS white/gray/black coloring to
// distinguish two cases that a naive "visited set" would conflate:
//
//   - BLACK (visited, fully expanded): shared ancestor — the same ID
//     appears via multiple paths in the DAG. This is normal data
//     (e.g. two strategies derived from the same parent). Skipped
//     silently to avoid duplicate subtrees in the result.
//   - GRAY (on recursion stack): true cycle — a back-edge to an
//     ancestor currently being expanded (e.g. A → B → A). Pathological
//     data, recorded in CyclesDetected so the agent can flag it.
//
// Without this distinction, every shared ancestor would be labeled a
// "cycle" — misleading for the agent.
//
// Missing parents (Get returns an error) are recorded in missing and
// the branch terminates — siblings continue to expand. Missing IDs are
// deduplicated so a parent missing via multiple paths appears once.
//
// Termination is guaranteed: every fetched ID is marked visited (black)
// before its parents are expanded, so it can be fetched at most once.
func (t *GetStrategyLineageTool) buildLineage(ctx context.Context, rootID string, maxDepth int) (*strategyLineageResult, error) {
	visited := make(map[string]bool) // black — ever fetched, never re-fetched
	onStack := make(map[string]bool) // gray — currently on recursion stack
	cycleSet := make(map[string]bool)
	missingSet := make(map[string]bool)
	var cycles, missing []string
	depthReached := 0

	// build is the recursive worker. Returns nil when the node could not
	// be constructed (cycle, missing, or already-expanded shared
	// ancestor). A non-nil node always has its Parents populated up to
	// maxDepth.
	var build func(id string, depth int) *strategyLineageNode
	build = func(id string, depth int) *strategyLineageNode {
		if onStack[id] {
			// True cycle: back-edge to an ancestor currently on the
			// recursion stack. Record once.
			if !cycleSet[id] {
				cycles = append(cycles, id)
				cycleSet[id] = true
			}
			return nil
		}
		if visited[id] {
			// Shared ancestor: already expanded via another path.
			// Skip silently — the result tree would have duplicated
			// the subtree, which is wasteful and confusing for the LLM.
			return nil
		}

		if depth > depthReached {
			depthReached = depth
		}

		gene, err := t.pool.Get(ctx, id)
		if err != nil {
			// Missing parent. Record once even if multiple children
			// point to the same missing ID.
			if !missingSet[id] {
				missing = append(missing, id)
				missingSet[id] = true
			}
			// Do NOT mark visited — a later retry might succeed if the
			// pool recovers. For now, the branch terminates.
			return nil
		}

		// Mark black (visited) before descending so any descendant that
		// references this ID is treated as a shared ancestor, not a cycle.
		visited[id] = true
		// Mark gray (on stack) so a back-edge from a descendant is
		// correctly classified as a cycle.
		onStack[id] = true
		defer func() { delete(onStack, id) }() // pop on return

		node := newLineageNode(gene)

		// Don't expand beyond maxDepth. The ParentIDs field on the node
		// still shows what would have been expanded, so the LLM can decide
		// to call again with a higher limit if it needs more depth.
		if depth >= maxDepth {
			return node
		}

		for _, parentID := range gene.ParentIDs {
			if parentID == "" {
				continue // defensive: skip empty entries
			}
			parentNode := build(parentID, depth+1)
			if parentNode != nil {
				node.Parents = append(node.Parents, parentNode)
			}
			// nil parentNode means cycle (recorded), missing (recorded),
			// or shared ancestor (intentionally silent) — in all cases
			// nothing to append.
		}
		return node
	}

	root := build(rootID, 0)
	if root == nil {
		// Root not found. Distinguish from "found but had errors" by
		// returning an error rather than an empty result — the caller
		// explicitly asked for THIS strategy's lineage.
		return nil, fmt.Errorf("strategy %q not found in gene pool", rootID)
	}

	result := &strategyLineageResult{
		Root:         root,
		DepthReached: depthReached,
		MaxDepth:     maxDepth,
	}
	if len(cycles) > 0 {
		result.CyclesDetected = cycles
	}
	if len(missing) > 0 {
		result.MissingParentIDs = missing
	}
	return result, nil
}

// newLineageNode converts a *gene_pool.StrategyGene into a lean
// strategyLineageNode, dropping heavy fields (Code, Params, Description,
// timestamps) to keep the JSON payload LLM-friendly. Nil slice fields
// are normalised to empty slices so the JSON encoding is stable
// ([] vs null).
func newLineageNode(gene *gene_pool.StrategyGene) *strategyLineageNode {
	factorIDs := gene.FactorIDs
	if factorIDs == nil {
		factorIDs = []string{}
	}
	parentIDs := gene.ParentIDs
	if parentIDs == nil {
		parentIDs = []string{}
	}
	return &strategyLineageNode{
		ID:           gene.ID,
		Name:         gene.Name,
		StrategyType: gene.StrategyType,
		FactorIDs:    factorIDs,
		ParentIDs:    parentIDs,
		Sharpe:       gene.Sharpe,
		Fitness:      gene.Fitness,
		Generation:   gene.Generation,
		Status:       gene.Status,
	}
}
