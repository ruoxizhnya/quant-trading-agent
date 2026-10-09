package evolution

import (
	"math/rand"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
)

// Crossover provides crossover operators for evolutionary algorithms.
type Crossover struct {
	rng           *rand.Rand
	crossoverRate float64
}

// NewCrossover creates a new Crossover operator.
func NewCrossover(rng *rand.Rand, rate float64) *Crossover {
	return &Crossover{
		rng:           rng,
		crossoverRate: rate,
	}
}

// StrategyCrossover performs crossover between two strategy genes.
func (c *Crossover) StrategyCrossover(parent1, parent2 *gene_pool.StrategyGene) (*gene_pool.StrategyGene, *gene_pool.StrategyGene) {
	if c.rng.Float64() > c.crossoverRate {
		// No crossover, return clones
		return c.clone(parent1), c.clone(parent2)
	}

	child1 := c.createChild(parent1, parent2)
	child2 := c.createChild(parent2, parent1)

	return child1, child2
}

// ParameterCrossover performs uniform crossover on parameter maps.
func (c *Crossover) ParameterCrossover(params1, params2 map[string]interface{}) (map[string]interface{}, map[string]interface{}) {
	child1 := make(map[string]interface{})
	child2 := make(map[string]interface{})

	// Get all keys
	allKeys := make(map[string]bool)
	for k := range params1 {
		allKeys[k] = true
	}
	for k := range params2 {
		allKeys[k] = true
	}

	for k := range allKeys {
		v1, ok1 := params1[k]
		v2, ok2 := params2[k]

		if !ok1 {
			child1[k] = v2
			child2[k] = v2
		} else if !ok2 {
			child1[k] = v1
			child2[k] = v1
		} else {
			// Both have the key, do uniform crossover
			if c.rng.Float64() < 0.5 {
				child1[k] = v1
				child2[k] = v2
			} else {
				child1[k] = v2
				child2[k] = v1
			}
		}
	}

	return child1, child2
}

// FactorCrossover performs crossover on factor ID lists.
func (c *Crossover) FactorCrossover(factors1, factors2 []string) ([]string, []string) {
	if len(factors1) == 0 && len(factors2) == 0 {
		return []string{}, []string{}
	}

	// Combine and deduplicate
	factorSet := make(map[string]bool)
	for _, f := range factors1 {
		factorSet[f] = true
	}
	for _, f := range factors2 {
		factorSet[f] = true
	}

	allFactors := make([]string, 0, len(factorSet))
	for f := range factorSet {
		allFactors = append(allFactors, f)
	}

	// Randomly assign to children
	child1 := make([]string, 0)
	child2 := make([]string, 0)

	for _, f := range allFactors {
		r := c.rng.Float64()
		if r < 0.33 {
			child1 = append(child1, f)
		} else if r < 0.66 {
			child2 = append(child2, f)
		} else {
			child1 = append(child1, f)
			child2 = append(child2, f)
		}
	}

	return child1, child2
}

// createChild creates a child from two parents.
func (c *Crossover) createChild(primary, secondary *gene_pool.StrategyGene) *gene_pool.StrategyGene {
	child := &gene_pool.StrategyGene{
		ID:           generateID(c.rng),
		Name:         primary.Name + "_x_" + secondary.Name,
		Description:  "Crossover of " + primary.Name + " and " + secondary.Name,
		StrategyType: primary.StrategyType,
		ParentIDs:    []string{primary.ID, secondary.ID},
		Status:       "evolving",
	}

	// Crossover parameters
	child.Params, _ = c.ParameterCrossover(primary.Params, secondary.Params)

	// Crossover factors
	child.FactorIDs, _ = c.FactorCrossover(primary.FactorIDs, secondary.FactorIDs)

	// Crossover code: 50% chance to take from either parent
	if c.rng.Float64() < 0.5 {
		child.Code = primary.Code
	} else {
		child.Code = secondary.Code
	}

	return child
}

// clone creates a deep copy of a strategy gene.
func (c *Crossover) clone(parent *gene_pool.StrategyGene) *gene_pool.StrategyGene {
	child := &gene_pool.StrategyGene{
		ID:           generateID(c.rng),
		Name:         parent.Name + "_clone",
		Description:  parent.Description,
		StrategyType: parent.StrategyType,
		Code:         parent.Code,
		ParentIDs:    []string{parent.ID},
		Status:       parent.Status,
	}

	// Copy params
	child.Params = make(map[string]interface{})
	for k, v := range parent.Params {
		child.Params[k] = v
	}

	// Copy factor IDs
	child.FactorIDs = make([]string, len(parent.FactorIDs))
	copy(child.FactorIDs, parent.FactorIDs)

	return child
}

func generateID(rng *rand.Rand) string {
	return "gene_" + string(rune('a'+rng.Intn(26))) + string(rune('a'+rng.Intn(26))) + string(rune('0'+rng.Intn(10))) + string(rune('0'+rng.Intn(10)))
}
