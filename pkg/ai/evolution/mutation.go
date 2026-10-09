package evolution

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
)

// Mutation provides mutation operators for evolutionary algorithms.
type Mutation struct {
	rng          *rand.Rand
	mutationRate float64
}

// NewMutation creates a new Mutation operator.
func NewMutation(rng *rand.Rand, rate float64) *Mutation {
	return &Mutation{
		rng:          rng,
		mutationRate: rate,
	}
}

// MutateStrategy applies mutations to a strategy gene.
func (m *Mutation) MutateStrategy(gene *gene_pool.StrategyGene, mutator *gene_pool.Mutator) {
	if m.rng.Float64() > m.mutationRate {
		return
	}

	// Mutate parameters
	if gene.Params != nil {
		m.mutateParams(gene.Params)
	}

	// Mutate code if mutator is available
	if mutator != nil && gene.Code != "" {
		mutated, _, err := mutator.Mutate(gene.Code)
		if err == nil && mutated != gene.Code {
			gene.Code = mutated
		}
	}

	// Mutate factor IDs (small chance)
	if m.rng.Float64() < 0.1 && len(gene.FactorIDs) > 0 {
		m.mutateFactorIDs(gene)
	}

	gene.UpdatedAt = time.Now()
}

// MutateParams applies mutation to parameter values.
func (m *Mutation) MutateParams(params map[string]interface{}) {
	for key, val := range params {
		if m.rng.Float64() > m.mutationRate {
			continue
		}

		switch v := val.(type) {
		case float64:
			// Gaussian mutation with 20% relative std
			std := v * 0.2
			if std == 0 {
				std = 0.1
			}
			params[key] = v + m.rng.NormFloat64()*std
		case int:
			// Integer mutation: add/subtract small non-zero value.
			// F1-new (ODR-012): the previous `Intn(5) - 2` produced
			// delta ∈ [-2, 2] with a 1/5 chance of 0, which made
			// the int param unchanged on every 5th invocation.
			// TestMutation_MutateParams uses a fixed seed (42) and
			// would intermittently fail in CI. Re-roll into {-3, -2,
			// -1, 1, 2, 3} (6 values) so the param is always mutated.
			delta := m.rng.Intn(3) + 1 // 1..3
			if m.rng.Intn(2) == 0 {
				delta = -delta
			}
			params[key] = v + delta
		case string:
			// String mutation: small chance to append suffix
			if m.rng.Float64() < 0.3 {
				params[key] = v + fmt.Sprintf("_%d", m.rng.Intn(100))
			}
		case bool:
			// Boolean mutation: flip with small probability
			if m.rng.Float64() < 0.2 {
				params[key] = !v
			}
		}
	}
}

// mutateParams applies parameter mutation with various strategies.
func (m *Mutation) mutateParams(params map[string]interface{}) {
	m.MutateParams(params)
}

// mutateFactorIDs modifies the factor ID list.
func (m *Mutation) mutateFactorIDs(gene *gene_pool.StrategyGene) {
	if len(gene.FactorIDs) == 0 {
		return
	}

	operation := m.rng.Intn(3)
	switch operation {
	case 0:
		// Remove a random factor
		if len(gene.FactorIDs) > 1 {
			idx := m.rng.Intn(len(gene.FactorIDs))
			gene.FactorIDs = append(gene.FactorIDs[:idx], gene.FactorIDs[idx+1:]...)
		}
	case 1:
		// Duplicate a random factor
		idx := m.rng.Intn(len(gene.FactorIDs))
		gene.FactorIDs = append(gene.FactorIDs, gene.FactorIDs[idx])
	case 2:
		// Shuffle factor order
		m.rng.Shuffle(len(gene.FactorIDs), func(i, j int) {
			gene.FactorIDs[i], gene.FactorIDs[j] = gene.FactorIDs[j], gene.FactorIDs[i]
		})
	}
}

// AdaptiveMutationRate adjusts mutation rate based on population diversity.
func (m *Mutation) AdaptiveMutationRate(diversity, targetDiversity float64) float64 {
	if diversity < targetDiversity*0.5 {
		// Low diversity: increase mutation rate
		return min(m.mutationRate*1.5, 0.5)
	} else if diversity > targetDiversity*2 {
		// High diversity: decrease mutation rate
		return max(m.mutationRate*0.5, 0.01)
	}
	return m.mutationRate
}

// min and max are provided by Go 1.21+ builtins (P0-10).
