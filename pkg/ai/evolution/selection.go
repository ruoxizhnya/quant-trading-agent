package evolution

import (
	"math/rand"
	"sort"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
)

// Selection provides selection operators for evolutionary algorithms.
type Selection struct {
	rng *rand.Rand
}

// NewSelection creates a new Selection operator.
func NewSelection(rng *rand.Rand) *Selection {
	return &Selection{rng: rng}
}

// TournamentSelect performs tournament selection.
func (s *Selection) TournamentSelect(population []*gene_pool.StrategyGene, count, tournamentSize int) []*gene_pool.StrategyGene {
	if len(population) == 0 {
		return nil
	}

	selected := make([]*gene_pool.StrategyGene, 0, count)
	for i := 0; i < count; i++ {
		winner := s.tournament(population, tournamentSize)
		if winner != nil {
			selected = append(selected, winner)
		}
	}
	return selected
}

// RouletteSelect performs roulette wheel (fitness proportionate) selection.
func (s *Selection) RouletteSelect(population []*gene_pool.StrategyGene, count int) []*gene_pool.StrategyGene {
	if len(population) == 0 {
		return nil
	}

	// Calculate total fitness (shift to positive if needed)
	minFitness := population[0].Fitness
	for _, ind := range population {
		if ind.Fitness < minFitness {
			minFitness = ind.Fitness
		}
	}

	var totalFitness float64
	shiftedFitness := make([]float64, len(population))
	for i, ind := range population {
		shiftedFitness[i] = ind.Fitness - minFitness + 1e-10
		totalFitness += shiftedFitness[i]
	}

	selected := make([]*gene_pool.StrategyGene, 0, count)
	for i := 0; i < count; i++ {
		spin := s.rng.Float64() * totalFitness
		var cumulative float64
		for j, fit := range shiftedFitness {
			cumulative += fit
			if cumulative >= spin {
				selected = append(selected, population[j])
				break
			}
		}
	}
	return selected
}

// RankSelect performs rank-based selection.
func (s *Selection) RankSelect(population []*gene_pool.StrategyGene, count int) []*gene_pool.StrategyGene {
	if len(population) == 0 {
		return nil
	}

	// Sort by fitness descending
	sorted := make([]*gene_pool.StrategyGene, len(population))
	copy(sorted, population)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Fitness > sorted[j].Fitness
	})

	// Calculate rank weights (linear)
	n := len(sorted)
	totalRank := float64(n * (n + 1) / 2)

	selected := make([]*gene_pool.StrategyGene, 0, count)
	for i := 0; i < count; i++ {
		spin := s.rng.Float64() * totalRank
		var cumulative float64
		for j := range sorted {
			cumulative += float64(n - j)
			if cumulative >= spin {
				selected = append(selected, sorted[j])
				break
			}
		}
	}
	return selected
}

// ElitistSelect selects the top N individuals by fitness.
func (s *Selection) ElitistSelect(population []*gene_pool.StrategyGene, count int) []*gene_pool.StrategyGene {
	if len(population) == 0 {
		return nil
	}

	sorted := make([]*gene_pool.StrategyGene, len(population))
	copy(sorted, population)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Fitness > sorted[j].Fitness
	})

	if count > len(sorted) {
		count = len(sorted)
	}
	return sorted[:count]
}

// tournament runs a single tournament.
func (s *Selection) tournament(population []*gene_pool.StrategyGene, size int) *gene_pool.StrategyGene {
	if len(population) == 0 {
		return nil
	}

	best := population[s.rng.Intn(len(population))]
	for i := 1; i < size; i++ {
		contender := population[s.rng.Intn(len(population))]
		if contender.Fitness > best.Fitness {
			best = contender
		}
	}
	return best
}
