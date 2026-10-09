package pipeline

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/agents"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading/pkg/expression"
)

// DiscoveryPipeline orchestrates the factor discovery process.
type DiscoveryPipeline struct {
	researchAgent *agents.ResearchAgent
	validateAgent *agents.ValidateAgent
	factorPool    *gene_pool.FactorPool
	mutator       *gene_pool.Mutator
	dataProvider  expression.DataProvider
}

// DiscoveryConfig configures the discovery pipeline.
type DiscoveryConfig struct {
	Topics             []string
	MutationsPerFactor int
	ValidationLevel    agents.ValidationLevel
	MinIC              float64
	BatchSize          int
}

// DiscoveryResult holds the results of a discovery run.
type DiscoveryResult struct {
	Generated  int                     `json:"generated"`
	Validated  int                     `json:"validated"`
	Rejected   int                     `json:"rejected"`
	TopFactors []*gene_pool.FactorGene `json:"top_factors"`
	Errors     []string                `json:"errors,omitempty"`
	Duration   time.Duration           `json:"duration"`
}

// NewDiscoveryPipeline creates a new discovery pipeline.
func NewDiscoveryPipeline(
	researchAgent *agents.ResearchAgent,
	validateAgent *agents.ValidateAgent,
	factorPool *gene_pool.FactorPool,
	dataProvider expression.DataProvider,
) *DiscoveryPipeline {
	return &DiscoveryPipeline{
		researchAgent: researchAgent,
		validateAgent: validateAgent,
		factorPool:    factorPool,
		mutator:       gene_pool.NewMutator(time.Now().UnixNano()),
		dataProvider:  dataProvider,
	}
}

// Run executes the full discovery pipeline.
func (p *DiscoveryPipeline) Run(ctx context.Context, config DiscoveryConfig) (*DiscoveryResult, error) {
	start := time.Now()
	result := &DiscoveryResult{
		TopFactors: []*gene_pool.FactorGene{},
		Errors:     []string{},
	}

	// Phase 1: Generate hypotheses from topics
	hypotheses := p.generateHypotheses(ctx, config.Topics)
	result.Generated = len(hypotheses)

	// Phase 2: Mutate factors
	mutated := p.mutateFactors(hypotheses, config.MutationsPerFactor)

	// Phase 3: Validate all factors
	validated, rejected := p.validateFactors(ctx, mutated, config.ValidationLevel, config.MinIC)
	result.Validated = validated
	result.Rejected = rejected

	// Phase 4: Save to gene pool
	if p.factorPool != nil {
		if err := p.saveToPool(ctx, mutated); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("save to pool failed: %v", err))
		}
	}

	// Collect top factors
	for _, gene := range mutated {
		if gene.Status == "validated" {
			result.TopFactors = append(result.TopFactors, gene)
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

// RunBatch runs discovery on a batch of topics concurrently.
func (p *DiscoveryPipeline) RunBatch(ctx context.Context, configs []DiscoveryConfig) ([]*DiscoveryResult, error) {
	var wg sync.WaitGroup
	results := make([]*DiscoveryResult, len(configs))
	var mu sync.Mutex
	var errs []string

	for i, config := range configs {
		wg.Add(1)
		go func(idx int, cfg DiscoveryConfig) {
			defer wg.Done()

			result, err := p.Run(ctx, cfg)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("batch %d: %v", idx, err))
				mu.Unlock()
			}
			results[idx] = result
		}(i, config)
	}

	wg.Wait()

	if len(errs) > 0 {
		return results, fmt.Errorf("batch errors: %v", errs)
	}
	return results, nil
}

// generateHypotheses generates factor hypotheses from topics.
func (p *DiscoveryPipeline) generateHypotheses(ctx context.Context, topics []string) []*gene_pool.FactorGene {
	var genes []*gene_pool.FactorGene

	for _, topic := range topics {
		hypothesis, err := p.researchAgent.GenerateHypothesis(ctx, topic)
		if err != nil {
			continue // Skip failed hypotheses
		}

		gene := &gene_pool.FactorGene{
			ID:          hypothesis.ID,
			Name:        hypothesis.Name,
			Category:    hypothesis.Category,
			Formula:     hypothesis.Formula,
			Description: hypothesis.Description,
			Rationale:   hypothesis.Rationale,
			Status:      "pending",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		genes = append(genes, gene)
	}

	return genes
}

// mutateFactors applies mutations to generate variants.
func (p *DiscoveryPipeline) mutateFactors(genes []*gene_pool.FactorGene, mutationsPerFactor int) []*gene_pool.FactorGene {
	var allGenes []*gene_pool.FactorGene

	// Include originals
	allGenes = append(allGenes, genes...)

	// Generate mutations
	for _, gene := range genes {
		for i := 0; i < mutationsPerFactor; i++ {
			mutatedFormula, mutType, err := p.mutator.Mutate(gene.Formula)
			if err != nil {
				continue
			}

			mutatedGene := &gene_pool.FactorGene{
				ID:          fmt.Sprintf("%s_mut%d_%s", gene.ID, i+1, mutType),
				Name:        fmt.Sprintf("%s (%s)", gene.Name, mutType),
				Category:    gene.Category,
				Formula:     mutatedFormula,
				Description: fmt.Sprintf("Mutation of %s: %s", gene.Name, mutType),
				ParentIDs:   []string{gene.ID},
				Generation:  gene.Generation + 1,
				Status:      "pending",
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}
			allGenes = append(allGenes, mutatedGene)
		}
	}

	return allGenes
}

// validateFactors validates all factors and returns counts.
func (p *DiscoveryPipeline) validateFactors(ctx context.Context, genes []*gene_pool.FactorGene, level agents.ValidationLevel, minIC float64) (int, int) {
	validated := 0
	rejected := 0

	for _, gene := range genes {
		result, err := p.validateAgent.ValidateGene(ctx, gene, level)
		if err != nil {
			gene.Status = "rejected"
			rejected++
			continue
		}

		if result.Passed && gene.IC >= minIC {
			gene.Status = "validated"
			validated++
		} else {
			gene.Status = "rejected"
			rejected++
		}
	}

	return validated, rejected
}

// saveToPool saves validated factors to the gene pool.
func (p *DiscoveryPipeline) saveToPool(ctx context.Context, genes []*gene_pool.FactorGene) error {
	for _, gene := range genes {
		if err := p.factorPool.Save(ctx, gene); err != nil {
			return fmt.Errorf("save gene %s: %w", gene.ID, err)
		}
	}
	return nil
}

// GetTopFactors retrieves the top N factors from the gene pool.
func (p *DiscoveryPipeline) GetTopFactors(ctx context.Context, n int) ([]*gene_pool.FactorGene, error) {
	if p.factorPool == nil {
		return nil, fmt.Errorf("factor pool not configured")
	}
	return p.factorPool.GetTopFactors(ctx, n)
}
