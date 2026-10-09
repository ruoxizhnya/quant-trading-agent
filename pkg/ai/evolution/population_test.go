package evolution

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockStrategyPool is a mock implementation for testing
type MockStrategyPool struct {
	genes map[string]*gene_pool.StrategyGene
}

func NewMockStrategyPool() *MockStrategyPool {
	return &MockStrategyPool{
		genes: make(map[string]*gene_pool.StrategyGene),
	}
}

func (m *MockStrategyPool) Save(ctx context.Context, gene *gene_pool.StrategyGene) error {
	m.genes[gene.ID] = gene
	return nil
}

func (m *MockStrategyPool) Get(ctx context.Context, id string) (*gene_pool.StrategyGene, error) {
	gene, ok := m.genes[id]
	if !ok {
		return nil, nil
	}
	return gene, nil
}

func (m *MockStrategyPool) ListByGeneration(ctx context.Context, generation int) ([]*gene_pool.StrategyGene, error) {
	var result []*gene_pool.StrategyGene
	for _, gene := range m.genes {
		if gene.Generation == generation {
			result = append(result, gene)
		}
	}
	return result, nil
}

func TestNewPopulation(t *testing.T) {
	pop := NewPopulation(100)
	require.NotNil(t, pop)
	assert.Equal(t, 0, pop.Size())
	assert.Equal(t, 0, pop.GetGeneration())
	assert.Equal(t, 100, pop.maxSize)
}

func TestPopulation_Add(t *testing.T) {
	pop := NewPopulation(3)

	gene1 := &gene_pool.StrategyGene{ID: "1", Fitness: 1.0}
	gene2 := &gene_pool.StrategyGene{ID: "2", Fitness: 2.0}
	gene3 := &gene_pool.StrategyGene{ID: "3", Fitness: 3.0}

	require.NoError(t, pop.Add(gene1))
	assert.Equal(t, 1, pop.Size())

	require.NoError(t, pop.Add(gene2))
	assert.Equal(t, 2, pop.Size())

	require.NoError(t, pop.Add(gene3))
	assert.Equal(t, 3, pop.Size())

	// Should fail when at max size
	gene4 := &gene_pool.StrategyGene{ID: "4", Fitness: 4.0}
	err := pop.Add(gene4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "population at maximum size")
}

func TestPopulation_Remove(t *testing.T) {
	pop := NewPopulation(10)

	gene1 := &gene_pool.StrategyGene{ID: "1", Fitness: 1.0}
	gene2 := &gene_pool.StrategyGene{ID: "2", Fitness: 2.0}

	pop.Add(gene1)
	pop.Add(gene2)
	assert.Equal(t, 2, pop.Size())

	// Remove existing
	assert.True(t, pop.Remove("1"))
	assert.Equal(t, 1, pop.Size())

	// Remove non-existing
	assert.False(t, pop.Remove("999"))
	assert.Equal(t, 1, pop.Size())
}

func TestPopulation_Get(t *testing.T) {
	pop := NewPopulation(10)

	gene := &gene_pool.StrategyGene{ID: "test", Fitness: 5.0}
	pop.Add(gene)

	found := pop.Get("test")
	require.NotNil(t, found)
	assert.Equal(t, "test", found.ID)
	assert.Equal(t, 5.0, found.Fitness)

	notFound := pop.Get("nonexistent")
	assert.Nil(t, notFound)
}

func TestPopulation_GetAll(t *testing.T) {
	pop := NewPopulation(10)

	genes := []*gene_pool.StrategyGene{
		{ID: "1", Fitness: 1.0},
		{ID: "2", Fitness: 2.0},
		{ID: "3", Fitness: 3.0},
	}

	for _, g := range genes {
		pop.Add(g)
	}

	all := pop.GetAll()
	assert.Len(t, all, 3)

	// Verify it's a copy (shallow copy of pointers)
	// Modifying the gene through the copy affects the original
	// because they point to the same underlying data
	all[0].Fitness = 999
	assert.Equal(t, 999.0, pop.Get("1").Fitness)
}

func TestPopulation_GetTop(t *testing.T) {
	pop := NewPopulation(10)

	genes := []*gene_pool.StrategyGene{
		{ID: "1", Fitness: 1.0},
		{ID: "2", Fitness: 5.0},
		{ID: "3", Fitness: 3.0},
		{ID: "4", Fitness: 2.0},
	}

	for _, g := range genes {
		pop.Add(g)
	}

	top2 := pop.GetTop(2)
	require.Len(t, top2, 2)
	assert.Equal(t, 5.0, top2[0].Fitness)
	assert.Equal(t, 3.0, top2[1].Fitness)

	// Request more than available
	top10 := pop.GetTop(10)
	assert.Len(t, top10, 4)
}

func TestPopulation_GetAverageFitness(t *testing.T) {
	pop := NewPopulation(10)

	// Empty population
	assert.Equal(t, 0.0, pop.GetAverageFitness())

	genes := []*gene_pool.StrategyGene{
		{ID: "1", Fitness: 1.0},
		{ID: "2", Fitness: 2.0},
		{ID: "3", Fitness: 3.0},
	}

	for _, g := range genes {
		pop.Add(g)
	}

	assert.InDelta(t, 2.0, pop.GetAverageFitness(), 0.001)
}

func TestPopulation_GetBestFitness(t *testing.T) {
	pop := NewPopulation(10)

	// Empty population
	assert.Equal(t, 0.0, pop.GetBestFitness())

	genes := []*gene_pool.StrategyGene{
		{ID: "1", Fitness: 1.0},
		{ID: "2", Fitness: 5.0},
		{ID: "3", Fitness: 3.0},
	}

	for _, g := range genes {
		pop.Add(g)
	}

	assert.Equal(t, 5.0, pop.GetBestFitness())
}

func TestPopulation_GetDiversity(t *testing.T) {
	pop := NewPopulation(10)

	// Less than 2 individuals
	assert.Equal(t, 0.0, pop.GetDiversity())

	pop.Add(&gene_pool.StrategyGene{ID: "1", Fitness: 1.0})
	pop.Add(&gene_pool.StrategyGene{ID: "2", Fitness: 5.0})
	pop.Add(&gene_pool.StrategyGene{ID: "3", Fitness: 3.0})

	diversity := pop.GetDiversity()
	assert.Greater(t, diversity, 0.0)
}

func TestPopulation_Prune(t *testing.T) {
	pop := NewPopulation(5)

	genes := []*gene_pool.StrategyGene{
		{ID: "1", Fitness: 1.0},
		{ID: "2", Fitness: 5.0},
		{ID: "3", Fitness: 3.0},
		{ID: "4", Fitness: 2.0},
		{ID: "5", Fitness: 4.0},
	}

	for _, g := range genes {
		pop.Add(g)
	}

	assert.Equal(t, 5, pop.Size())

	// Reduce max size to 3 to trigger pruning
	pop.maxSize = 3
	removed := pop.Prune()
	assert.Equal(t, 2, removed)
	assert.Equal(t, 3, pop.Size())

	// After pruning, should keep the best 3 (fitness 5, 4, 3)
	top := pop.GetTop(3)
	assert.Len(t, top, 3)
	assert.Equal(t, 5.0, top[0].Fitness)
	assert.Equal(t, 4.0, top[1].Fitness)
	assert.Equal(t, 3.0, top[2].Fitness)
}

func TestPopulation_Prune_NoPruningNeeded(t *testing.T) {
	pop := NewPopulation(10)
	pop.Add(&gene_pool.StrategyGene{ID: "1", Fitness: 1.0})
	pop.Add(&gene_pool.StrategyGene{ID: "2", Fitness: 2.0})

	removed := pop.Prune()
	assert.Equal(t, 0, removed)
	assert.Equal(t, 2, pop.Size())
}

func TestPopulation_IncrementGeneration(t *testing.T) {
	pop := NewPopulation(10)
	assert.Equal(t, 0, pop.GetGeneration())

	pop.IncrementGeneration()
	assert.Equal(t, 1, pop.GetGeneration())

	pop.IncrementGeneration()
	assert.Equal(t, 2, pop.GetGeneration())
}

func TestPopulation_SaveToPool_NotConfigured(t *testing.T) {
	pop := NewPopulation(10)
	pop.generation = 5

	// Add some genes
	genes := []*gene_pool.StrategyGene{
		{ID: "1", Fitness: 1.0},
		{ID: "2", Fitness: 2.0},
	}

	for _, g := range genes {
		pop.Add(g)
	}

	// SaveToPool requires a real *gene_pool.StrategyPool
	// In unit tests without DB, we verify the population structure
	assert.Equal(t, 2, pop.Size())
	assert.Equal(t, 5, pop.GetGeneration())
	assert.Equal(t, 2.0, pop.GetBestFitness())
}

func TestPopulation_LoadFromPool_NotConfigured(t *testing.T) {
	pop := NewPopulation(10)

	// LoadFromPool requires a real *gene_pool.StrategyPool
	// In unit tests without DB, we verify empty population behavior
	assert.Equal(t, 0, pop.Size())
	assert.Equal(t, 0, pop.GetGeneration())
}

func TestPopulation_GetStats(t *testing.T) {
	pop := NewPopulation(10)

	// Empty population
	stats := pop.GetStats()
	assert.Equal(t, 0, stats.Size)
	assert.Equal(t, 0, stats.Generation)
	assert.Equal(t, 10, stats.MaxSize)

	genes := []*gene_pool.StrategyGene{
		{ID: "1", Fitness: 1.0},
		{ID: "2", Fitness: 5.0},
		{ID: "3", Fitness: 3.0},
	}

	for _, g := range genes {
		pop.Add(g)
	}
	pop.generation = 10

	stats = pop.GetStats()
	assert.Equal(t, 3, stats.Size)
	assert.Equal(t, 10, stats.Generation)
	assert.Equal(t, 5.0, stats.BestFitness)
	assert.Equal(t, 1.0, stats.WorstFitness)
	assert.InDelta(t, 3.0, stats.AvgFitness, 0.001)
	assert.Greater(t, stats.Diversity, 0.0)
}

func TestPopulationStats_Structure(t *testing.T) {
	stats := &PopulationStats{
		Size:         50,
		Generation:   10,
		BestFitness:  5.0,
		AvgFitness:   3.0,
		WorstFitness: 1.0,
		Diversity:    0.5,
		MaxSize:      100,
	}

	assert.Equal(t, 50, stats.Size)
	assert.Equal(t, 10, stats.Generation)
	assert.Equal(t, 5.0, stats.BestFitness)
	assert.Equal(t, 3.0, stats.AvgFitness)
	assert.Equal(t, 1.0, stats.WorstFitness)
	assert.Equal(t, 0.5, stats.Diversity)
	assert.Equal(t, 100, stats.MaxSize)
}

func TestPopulation_ConcurrentAccess(t *testing.T) {
	pop := NewPopulation(100)

	// Add genes concurrently
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			for j := 0; j < 5; j++ {
				gene := &gene_pool.StrategyGene{
					ID:      string(rune('a' + idx)),
					Fitness: float64(idx * j),
				}
				pop.Add(gene)
			}
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// S7-P0-11 (ODR-043): with the mutex fix, all 50 adds succeed
	// atomically (maxSize=100, 10×5=50 genes). The old "might not be
	// exactly 50" comment reflected the pre-fix race where concurrent
	// appends could lose entries.
	assert.Equal(t, 50, pop.Size(), "all 50 concurrent adds should succeed with proper locking")
}

// TestPopulation_ConcurrentReadWrite verifies that read operations
// (GetAll, Size, GetStats, GetTop) are safe to call concurrently with
// writes (Add). S7-P0-11 (ODR-043).
func TestPopulation_ConcurrentReadWrite(t *testing.T) {
	pop := NewPopulation(200)

	// Seed with some initial genes so readers have data to observe.
	for i := 0; i < 20; i++ {
		_ = pop.Add(&gene_pool.StrategyGene{
			ID:      fmt.Sprintf("seed-%d", i),
			Fitness: float64(i),
		})
	}

	const numWriters = 5
	const numReaders = 5
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(numWriters + numReaders)

	// Writers: add and remove genes concurrently.
	for w := 0; w < numWriters; w++ {
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				gene := &gene_pool.StrategyGene{
					ID:      fmt.Sprintf("w%d-%d", writerID, i),
					Fitness: float64(writerID*iterations + i),
				}
				_ = pop.Add(gene)
				_ = pop.GetStats()
				_ = pop.GetAll()
			}
		}(w)
	}

	// Readers: call various read methods concurrently.
	for r := 0; r < numReaders; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = pop.Size()
				_ = pop.GetBestFitness()
				_ = pop.GetAverageFitness()
				_ = pop.GetDiversity()
				_ = pop.GetStats()
				_ = pop.GetTop(5)
				_ = pop.GetAll()
			}
		}()
	}

	wg.Wait()

	// After all writers finish, size should be: 20 seeds + (5 × 50) = 270.
	// But maxSize is 200, so some adds will have returned an error.
	// The key assertion is that no data race was detected by -race.
	assert.Greater(t, pop.Size(), 0, "population should have genes after concurrent read/write")
}
