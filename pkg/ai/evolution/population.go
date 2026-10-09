package evolution

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
)

// Population manages a population of strategy genes.
//
// All methods are safe for concurrent use. Read operations (Get, GetAll,
// GetTop, Size, GetGeneration, GetAverageFitness, GetBestFitness,
// GetDiversity, GetStats) acquire a read lock; write operations (Add,
// Remove, Prune, IncrementGeneration, LoadFromPool, SaveToPool) acquire
// a write lock.
type Population struct {
	mu          sync.RWMutex
	individuals []*gene_pool.StrategyGene
	generation  int
	maxSize     int
}

// NewPopulation creates a new population.
func NewPopulation(maxSize int) *Population {
	return &Population{
		individuals: make([]*gene_pool.StrategyGene, 0, maxSize),
		generation:  0,
		maxSize:     maxSize,
	}
}

// Add adds an individual to the population.
func (p *Population) Add(gene *gene_pool.StrategyGene) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.individuals) >= p.maxSize {
		return fmt.Errorf("population at maximum size: %d", p.maxSize)
	}
	p.individuals = append(p.individuals, gene)
	return nil
}

// Remove removes an individual by ID.
func (p *Population) Remove(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, ind := range p.individuals {
		if ind.ID == id {
			p.individuals = append(p.individuals[:i], p.individuals[i+1:]...)
			return true
		}
	}
	return false
}

// Get returns an individual by ID.
func (p *Population) Get(id string) *gene_pool.StrategyGene {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, ind := range p.individuals {
		if ind.ID == id {
			return ind
		}
	}
	return nil
}

// GetAll returns all individuals.
func (p *Population) GetAll() []*gene_pool.StrategyGene {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]*gene_pool.StrategyGene, len(p.individuals))
	copy(result, p.individuals)
	return result
}

// GetTop returns the top N individuals by fitness.
func (p *Population) GetTop(n int) []*gene_pool.StrategyGene {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sortByFitnessLocked()
	if n > len(p.individuals) {
		n = len(p.individuals)
	}
	result := make([]*gene_pool.StrategyGene, n)
	copy(result, p.individuals[:n])
	return result
}

// Size returns the current population size.
func (p *Population) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.individuals)
}

// GetGeneration returns the current generation number.
func (p *Population) GetGeneration() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.generation
}

// IncrementGeneration increments the generation counter.
func (p *Population) IncrementGeneration() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation++
}

// GetAverageFitness returns the average fitness of the population.
func (p *Population) GetAverageFitness() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.individuals) == 0 {
		return 0
	}
	var sum float64
	for _, ind := range p.individuals {
		sum += ind.Fitness
	}
	return sum / float64(len(p.individuals))
}

// GetBestFitness returns the best fitness in the population.
func (p *Population) GetBestFitness() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.individuals) == 0 {
		return 0
	}
	best := p.individuals[0].Fitness
	for _, ind := range p.individuals[1:] {
		if ind.Fitness > best {
			best = ind.Fitness
		}
	}
	return best
}

// GetDiversity returns a measure of population diversity.
func (p *Population) GetDiversity() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.individuals) < 2 {
		return 0
	}

	// Calculate average pairwise fitness difference
	var totalDiff float64
	count := 0
	for i := 0; i < len(p.individuals); i++ {
		for j := i + 1; j < len(p.individuals); j++ {
			diff := p.individuals[i].Fitness - p.individuals[j].Fitness
			if diff < 0 {
				diff = -diff
			}
			totalDiff += diff
			count++
		}
	}

	if count == 0 {
		return 0
	}

	return totalDiff / float64(count)
}

// Prune removes the worst individuals to maintain max size.
func (p *Population) Prune() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.individuals) <= p.maxSize {
		return 0
	}

	p.sortByFitnessLocked()
	removed := len(p.individuals) - p.maxSize
	p.individuals = p.individuals[:p.maxSize]
	return removed
}

// SaveToPool persists the population to the gene pool.
//
// This acquires a write lock because it mutates each gene's Generation
// and UpdatedAt fields before persisting.
func (p *Population) SaveToPool(ctx context.Context, pool *gene_pool.StrategyPool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ind := range p.individuals {
		ind.Generation = p.generation
		ind.UpdatedAt = time.Now()
		if err := pool.Save(ctx, ind); err != nil {
			return fmt.Errorf("save individual %s: %w", ind.ID, err)
		}
	}
	return nil
}

// LoadFromPool loads individuals from the gene pool.
func (p *Population) LoadFromPool(ctx context.Context, pool *gene_pool.StrategyPool, generation int) error {
	genes, err := pool.ListByGeneration(ctx, generation)
	if err != nil {
		return fmt.Errorf("load generation %d: %w", generation, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, gene := range genes {
		if len(p.individuals) >= p.maxSize {
			break
		}
		p.individuals = append(p.individuals, gene)
	}

	p.generation = generation
	return nil
}

// sortByFitnessLocked sorts individuals by fitness descending.
//
// Caller must hold p.mu (write lock).
func (p *Population) sortByFitnessLocked() {
	sort.Slice(p.individuals, func(i, j int) bool {
		return p.individuals[i].Fitness > p.individuals[j].Fitness
	})
}

// PopulationStats holds statistics about the population.
type PopulationStats struct {
	Size         int     `json:"size"`
	Generation   int     `json:"generation"`
	BestFitness  float64 `json:"best_fitness"`
	AvgFitness   float64 `json:"avg_fitness"`
	WorstFitness float64 `json:"worst_fitness"`
	Diversity    float64 `json:"diversity"`
	MaxSize      int     `json:"max_size"`
}

// GetStats returns population statistics.
func (p *Population) GetStats() *PopulationStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.individuals) == 0 {
		return &PopulationStats{
			Size:       0,
			Generation: p.generation,
			MaxSize:    p.maxSize,
		}
	}

	best := p.individuals[0].Fitness
	worst := p.individuals[0].Fitness
	sum := 0.0

	for _, ind := range p.individuals {
		if ind.Fitness > best {
			best = ind.Fitness
		}
		if ind.Fitness < worst {
			worst = ind.Fitness
		}
		sum += ind.Fitness
	}

	return &PopulationStats{
		Size:         len(p.individuals),
		Generation:   p.generation,
		BestFitness:  best,
		AvgFitness:   sum / float64(len(p.individuals)),
		WorstFitness: worst,
		// GetDiversity acquires the read lock itself; call the locked
		// variant to avoid self-deadlock since we already hold the lock.
		Diversity: p.getDiversityLocked(),
		MaxSize:   p.maxSize,
	}
}

// getDiversityLocked is the lock-free variant of GetDiversity.
//
// Caller must hold p.mu (read or write lock).
func (p *Population) getDiversityLocked() float64 {
	if len(p.individuals) < 2 {
		return 0
	}

	var totalDiff float64
	count := 0
	for i := 0; i < len(p.individuals); i++ {
		for j := i + 1; j < len(p.individuals); j++ {
			diff := p.individuals[i].Fitness - p.individuals[j].Fitness
			if diff < 0 {
				diff = -diff
			}
			totalDiff += diff
			count++
		}
	}

	if count == 0 {
		return 0
	}

	return totalDiff / float64(count)
}
