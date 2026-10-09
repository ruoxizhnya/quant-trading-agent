package agents

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/evolution"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/search"
)

// EvolveAgent manages the evolutionary process for strategy optimization.
type EvolveAgent struct {
	population  *evolution.Population
	geneticOpt  *search.GeneticOptimizer
	selection   *evolution.Selection
	crossover   *evolution.Crossover
	mutation    *evolution.Mutation
	pool        *gene_pool.StrategyPool
	factorPool  *gene_pool.FactorPool
	mutator     *gene_pool.Mutator
	fitnessFunc func(*gene_pool.StrategyGene) float64
	config      EvolveConfig
	rng         *rand.Rand
}

// EvolveConfig holds configuration for the evolution process.
type EvolveConfig struct {
	PopulationSize    int
	MaxGenerations    int
	EliteCount        int
	CrossoverRate     float64
	MutationRate      float64
	MinFitness        float64
	ConvergenceGens   int
	ConvergenceThresh float64
	Seed              int64
}

// DefaultEvolveConfig returns default evolution configuration.
func DefaultEvolveConfig() EvolveConfig {
	return EvolveConfig{
		PopulationSize:    50,
		MaxGenerations:    100,
		EliteCount:        5,
		CrossoverRate:     0.8,
		MutationRate:      0.15,
		MinFitness:        0.0,
		ConvergenceGens:   20,
		ConvergenceThresh: 0.001,
		Seed:              time.Now().UnixNano(),
	}
}

// EvolutionResult holds the result of an evolution run.
type EvolutionResult struct {
	BestStrategy *gene_pool.StrategyGene
	FinalGen     int
	Generations  []GenerationStats
	Converged    bool
	Termination  string
	Duration     time.Duration
}

// GenerationStats holds statistics for a generation.
type GenerationStats struct {
	Generation   int
	BestFitness  float64
	AvgFitness   float64
	WorstFitness float64
	Diversity    float64
	Population   int
}

// NewEvolveAgent creates a new evolution agent.
func NewEvolveAgent(pool *gene_pool.StrategyPool, factorPool *gene_pool.FactorPool, config EvolveConfig) *EvolveAgent {
	rng := rand.New(rand.NewSource(config.Seed))
	return &EvolveAgent{
		population: evolution.NewPopulation(config.PopulationSize),
		geneticOpt: search.NewGeneticOptimizer(config.Seed),
		selection:  evolution.NewSelection(rng),
		crossover:  evolution.NewCrossover(rng, config.CrossoverRate),
		mutation:   evolution.NewMutation(rng, config.MutationRate),
		pool:       pool,
		factorPool: factorPool,
		mutator:    gene_pool.NewMutator(config.Seed),
		config:     config,
		rng:        rng,
	}
}

// SetFitnessFunction sets the fitness evaluation function.
func (a *EvolveAgent) SetFitnessFunction(fn func(*gene_pool.StrategyGene) float64) {
	a.fitnessFunc = fn
}

// InitializePopulation creates an initial population from existing strategies or randomly.
func (a *EvolveAgent) InitializePopulation(ctx context.Context, existingIDs []string) error {
	// Load existing strategies if provided
	if len(existingIDs) > 0 {
		for _, id := range existingIDs {
			gene, err := a.pool.Get(ctx, id)
			if err != nil {
				continue
			}
			gene.Generation = 0
			gene.ParentIDs = nil
			if err := a.population.Add(gene); err != nil {
				break
			}
		}
	}

	// Fill remaining with random mutations of top strategies from pool
	if a.population.Size() < a.config.PopulationSize {
		topGenes, err := a.pool.GetTopStrategies(ctx, a.config.PopulationSize-a.population.Size())
		if err == nil {
			for _, gene := range topGenes {
				if a.population.Size() >= a.config.PopulationSize {
					break
				}
				mutated := a.createMutatedOffspring(gene)
				mutated.Generation = 0
				mutated.ParentIDs = []string{gene.ID}
				if err := a.population.Add(mutated); err != nil {
					break
				}
			}
		}
	}

	// Evaluate fitness for all individuals
	if a.fitnessFunc != nil {
		for _, ind := range a.population.GetAll() {
			ind.Fitness = a.fitnessFunc(ind)
		}
	}

	return nil
}

// Evolve runs the evolutionary process.
func (a *EvolveAgent) Evolve(ctx context.Context) (*EvolutionResult, error) {
	start := time.Now()
	result := &EvolutionResult{
		Generations: make([]GenerationStats, 0, a.config.MaxGenerations),
		Termination: "max_generations",
	}

	if a.population.Size() == 0 {
		return nil, fmt.Errorf("population is empty, call InitializePopulation first")
	}

	if a.fitnessFunc == nil {
		return nil, fmt.Errorf("fitness function not set")
	}

	// Evaluate initial population
	for _, ind := range a.population.GetAll() {
		if ind.Fitness == 0 {
			ind.Fitness = a.fitnessFunc(ind)
		}
	}

	bestFitness := -999999.0
	stagnantGens := 0

	for gen := 0; gen < a.config.MaxGenerations; gen++ {
		a.population.IncrementGeneration()
		currentGen := a.population.GetGeneration()

		// Selection
		selected := a.selection.TournamentSelect(a.population.GetAll(), a.config.PopulationSize, 3)

		// Create new population
		newPopulation := evolution.NewPopulation(a.config.PopulationSize)

		// Elitism: keep best individuals
		topIndividuals := a.population.GetTop(a.config.EliteCount)
		for _, elite := range topIndividuals {
			eliteCopy := *elite
			eliteCopy.ParentIDs = nil
			newPopulation.Add(&eliteCopy)
		}

		// Generate offspring
		for newPopulation.Size() < a.config.PopulationSize {
			if len(selected) < 2 {
				break
			}

			// Select parents
			parent1 := selected[a.rng.Intn(len(selected))]
			parent2 := selected[a.rng.Intn(len(selected))]

			// Crossover
			child1, child2 := a.crossover.StrategyCrossover(parent1, parent2)

			// Mutation
			a.mutation.MutateStrategy(child1, a.mutator)
			a.mutation.MutateStrategy(child2, a.mutator)

			// Evaluate fitness
			child1.Fitness = a.fitnessFunc(child1)
			child2.Fitness = a.fitnessFunc(child2)

			child1.Generation = currentGen
			child2.Generation = currentGen

			newPopulation.Add(child1)
			if newPopulation.Size() < a.config.PopulationSize {
				newPopulation.Add(child2)
			}
		}

		// Replace old population
		a.population = newPopulation

		// Calculate generation stats
		stats := a.calculateGenerationStats(currentGen)
		result.Generations = append(result.Generations, stats)

		// Check convergence
		if stats.BestFitness > bestFitness+a.config.ConvergenceThresh {
			bestFitness = stats.BestFitness
			stagnantGens = 0
		} else {
			stagnantGens++
		}

		// Check termination conditions
		if stats.BestFitness >= a.config.MinFitness && a.config.MinFitness > 0 {
			result.Termination = "fitness_threshold"
			result.Converged = true
			break
		}

		if stagnantGens >= a.config.ConvergenceGens {
			result.Termination = "convergence"
			result.Converged = true
			break
		}

		// Save to pool periodically
		if gen%10 == 0 || gen == a.config.MaxGenerations-1 {
			a.population.SaveToPool(ctx, a.pool)
		}
	}

	result.FinalGen = a.population.GetGeneration()
	result.BestStrategy = a.population.GetTop(1)[0]
	result.Duration = time.Since(start)

	// Final save
	a.population.SaveToPool(ctx, a.pool)

	return result, nil
}

// GetPopulation returns the current population.
func (a *EvolveAgent) GetPopulation() *evolution.Population {
	return a.population
}

// GetBestStrategy returns the best strategy in the current population.
func (a *EvolveAgent) GetBestStrategy() *gene_pool.StrategyGene {
	top := a.population.GetTop(1)
	if len(top) == 0 {
		return nil
	}
	return top[0]
}

// calculateGenerationStats calculates statistics for the current generation.
func (a *EvolveAgent) calculateGenerationStats(gen int) GenerationStats {
	all := a.population.GetAll()
	if len(all) == 0 {
		return GenerationStats{Generation: gen}
	}

	best := all[0].Fitness
	worst := all[0].Fitness
	sum := 0.0

	for _, ind := range all {
		if ind.Fitness > best {
			best = ind.Fitness
		}
		if ind.Fitness < worst {
			worst = ind.Fitness
		}
		sum += ind.Fitness
	}

	return GenerationStats{
		Generation:   gen,
		BestFitness:  best,
		AvgFitness:   sum / float64(len(all)),
		WorstFitness: worst,
		Diversity:    a.population.GetDiversity(),
		Population:   len(all),
	}
}

// createMutatedOffspring creates a mutated copy of a strategy gene.
func (a *EvolveAgent) createMutatedOffspring(parent *gene_pool.StrategyGene) *gene_pool.StrategyGene {
	child := &gene_pool.StrategyGene{
		ID:           fmt.Sprintf("gene_%d", a.rng.Int63()),
		Name:         parent.Name + "_mut",
		Description:  parent.Description,
		StrategyType: parent.StrategyType,
		Code:         parent.Code,
		Params:       make(map[string]interface{}),
		FactorIDs:    make([]string, len(parent.FactorIDs)),
		ParentIDs:    []string{parent.ID},
		Status:       "evolving",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	// Copy params
	for k, v := range parent.Params {
		child.Params[k] = v
	}
	copy(child.FactorIDs, parent.FactorIDs)

	// Mutate parameters
	for key, val := range child.Params {
		switch v := val.(type) {
		case float64:
			child.Params[key] = v + (a.rng.Float64()-0.5)*v*0.2
		case int:
			child.Params[key] = v + a.rng.Intn(5) - 2
		}
	}

	return child
}
