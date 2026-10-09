package search

import (
	"math"
	"math/rand"
	"sort"
)

// GeneticOptimizer implements a genetic algorithm for strategy optimization.
type GeneticOptimizer struct {
	rng            *rand.Rand
	populationSize int
	generations    int
	crossoverRate  float64
	mutationRate   float64
	eliteCount     int
}

// NewGeneticOptimizer creates a new genetic optimizer.
func NewGeneticOptimizer(seed int64) *GeneticOptimizer {
	return &GeneticOptimizer{
		rng:            rand.New(rand.NewSource(seed)),
		populationSize: 50,
		generations:    100,
		crossoverRate:  0.8,
		mutationRate:   0.1,
		eliteCount:     5,
	}
}

// Individual represents a candidate solution.
type Individual struct {
	Genes   []float64 `json:"genes"`
	Fitness float64   `json:"fitness"`
	Rank    int       `json:"rank"`
}

// Optimize runs the genetic algorithm.
func (g *GeneticOptimizer) Optimize(fitnessFunc func([]float64) float64, geneLength int, geneMin, geneMax float64) *Individual {
	// Initialize population
	population := g.initializePopulation(geneLength, geneMin, geneMax)

	// Evaluate initial population
	for _, ind := range population {
		ind.Fitness = fitnessFunc(ind.Genes)
	}

	// Evolution loop
	for gen := 0; gen < g.generations; gen++ {
		// Sort by fitness (descending)
		sort.Slice(population, func(i, j int) bool {
			return population[i].Fitness > population[j].Fitness
		})

		// Create new population
		newPopulation := make([]*Individual, 0, g.populationSize)

		// Elitism: keep top individuals
		for i := 0; i < g.eliteCount && i < len(population); i++ {
			newPopulation = append(newPopulation, &Individual{
				Genes:   append([]float64(nil), population[i].Genes...),
				Fitness: population[i].Fitness,
			})
		}

		// Generate offspring
		for len(newPopulation) < g.populationSize {
			// Selection
			parent1 := g.tournamentSelection(population)
			parent2 := g.tournamentSelection(population)

			// Crossover
			child1, child2 := g.crossover(parent1, parent2)

			// Mutation
			g.mutate(child1, geneMin, geneMax)
			g.mutate(child2, geneMin, geneMax)

			// Evaluate
			child1.Fitness = fitnessFunc(child1.Genes)
			child2.Fitness = fitnessFunc(child2.Genes)

			newPopulation = append(newPopulation, child1, child2)
		}

		// Trim to population size
		population = newPopulation[:g.populationSize]
	}

	// Return best individual
	sort.Slice(population, func(i, j int) bool {
		return population[i].Fitness > population[j].Fitness
	})

	return population[0]
}

// initializePopulation creates an initial random population.
func (g *GeneticOptimizer) initializePopulation(geneLength int, geneMin, geneMax float64) []*Individual {
	population := make([]*Individual, g.populationSize)
	for i := range population {
		genes := make([]float64, geneLength)
		for j := range genes {
			genes[j] = g.rng.Float64()*(geneMax-geneMin) + geneMin
		}
		population[i] = &Individual{Genes: genes}
	}
	return population
}

// tournamentSelection selects an individual using tournament selection.
func (g *GeneticOptimizer) tournamentSelection(population []*Individual) *Individual {
	tournamentSize := 3
	best := population[g.rng.Intn(len(population))]

	for i := 1; i < tournamentSize; i++ {
		contender := population[g.rng.Intn(len(population))]
		if contender.Fitness > best.Fitness {
			best = contender
		}
	}

	return best
}

// crossover performs uniform crossover between two parents.
func (g *GeneticOptimizer) crossover(parent1, parent2 *Individual) (*Individual, *Individual) {
	child1 := &Individual{Genes: make([]float64, len(parent1.Genes))}
	child2 := &Individual{Genes: make([]float64, len(parent1.Genes))}

	if g.rng.Float64() > g.crossoverRate {
		// No crossover
		copy(child1.Genes, parent1.Genes)
		copy(child2.Genes, parent2.Genes)
		return child1, child2
	}

	for i := range parent1.Genes {
		if g.rng.Float64() < 0.5 {
			child1.Genes[i] = parent1.Genes[i]
			child2.Genes[i] = parent2.Genes[i]
		} else {
			child1.Genes[i] = parent2.Genes[i]
			child2.Genes[i] = parent1.Genes[i]
		}
	}

	return child1, child2
}

// mutate applies Gaussian mutation to an individual.
func (g *GeneticOptimizer) mutate(individual *Individual, geneMin, geneMax float64) {
	for i := range individual.Genes {
		if g.rng.Float64() < g.mutationRate {
			// Gaussian mutation
			std := (geneMax - geneMin) / 10.0
			individual.Genes[i] += g.rng.NormFloat64() * std

			// Clip to bounds
			individual.Genes[i] = math.Max(geneMin, math.Min(geneMax, individual.Genes[i]))
		}
	}
}

// GetPopulationStats returns statistics about the population.
func (g *GeneticOptimizer) GetPopulationStats(population []*Individual) map[string]float64 {
	if len(population) == 0 {
		return map[string]float64{}
	}

	best := population[0].Fitness
	worst := population[0].Fitness
	sum := 0.0

	for _, ind := range population {
		if ind.Fitness > best {
			best = ind.Fitness
		}
		if ind.Fitness < worst {
			worst = ind.Fitness
		}
		sum += ind.Fitness
	}

	return map[string]float64{
		"best":      best,
		"worst":     worst,
		"mean":      sum / float64(len(population)),
		"diversity": best - worst,
	}
}
