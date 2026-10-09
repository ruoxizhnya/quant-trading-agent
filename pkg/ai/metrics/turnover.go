package metrics

import (
	"math"

	"github.com/ruoxizhnya/quant-trading/pkg/statistics"
)

// TurnoverCalculator computes portfolio turnover metrics.
type TurnoverCalculator struct{}

// NewTurnoverCalculator creates a new TurnoverCalculator.
func NewTurnoverCalculator() *TurnoverCalculator {
	return &TurnoverCalculator{}
}

// TurnoverResult holds turnover calculation results.
type TurnoverResult struct {
	MeanTurnover   float64   `json:"mean_turnover"`
	MaxTurnover    float64   `json:"max_turnover"`
	MinTurnover    float64   `json:"min_turnover"`
	TurnoverSeries []float64 `json:"turnover_series"`
}

// ComputeTurnover calculates turnover between consecutive portfolio weight vectors.
func (c *TurnoverCalculator) ComputeTurnover(weightsByDate []map[string]float64) *TurnoverResult {
	if len(weightsByDate) < 2 {
		return &TurnoverResult{}
	}

	var turnovers []float64
	for i := 1; i < len(weightsByDate); i++ {
		prev := weightsByDate[i-1]
		curr := weightsByDate[i]

		turnover := computeSingleTurnover(prev, curr)
		if !math.IsNaN(turnover) {
			turnovers = append(turnovers, turnover)
		}
	}

	if len(turnovers) == 0 {
		return &TurnoverResult{}
	}

	return &TurnoverResult{
		MeanTurnover:   statistics.Mean(turnovers),
		MaxTurnover:    maxIn(turnovers),
		MinTurnover:    minIn(turnovers),
		TurnoverSeries: turnovers,
	}
}

// ComputeTurnoverFromRanks calculates turnover from rank-based portfolios.
func (c *TurnoverCalculator) ComputeTurnoverFromRanks(ranksByDate []map[string]int, topN int) *TurnoverResult {
	if len(ranksByDate) < 2 {
		return &TurnoverResult{}
	}

	var weightsByDate []map[string]float64
	for _, ranks := range ranksByDate {
		weights := ranksToWeights(ranks, topN)
		weightsByDate = append(weightsByDate, weights)
	}

	return c.ComputeTurnover(weightsByDate)
}

// computeSingleTurnover calculates turnover between two weight vectors.
func computeSingleTurnover(prev, curr map[string]float64) float64 {
	allSymbols := make(map[string]struct{})
	for s := range prev {
		allSymbols[s] = struct{}{}
	}
	for s := range curr {
		allSymbols[s] = struct{}{}
	}

	var turnover float64
	for s := range allSymbols {
		w1 := prev[s]
		w2 := curr[s]
		turnover += math.Abs(w2 - w1)
	}

	return turnover / 2.0
}

// ranksToWeights converts ranks to equal weights for top-N stocks.
func ranksToWeights(ranks map[string]int, topN int) map[string]float64 {
	weights := make(map[string]float64)

	// Find stocks in top N
	var topStocks []string
	for symbol, rank := range ranks {
		if rank <= topN {
			topStocks = append(topStocks, symbol)
		}
	}

	if len(topStocks) == 0 {
		return weights
	}

	weight := 1.0 / float64(len(topStocks))
	for _, s := range topStocks {
		weights[s] = weight
	}

	return weights
}

// maxIn returns the maximum value in a slice. Renamed from
// `max` to avoid shadowing the Go 1.21+ builtin (Sprint 6 P0-10).
func maxIn(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	m := data[0]
	for _, v := range data[1:] {
		m = max(m, v)
	}
	return m
}

// minIn returns the minimum value in a slice. Renamed from
// `min` to avoid shadowing the Go 1.21+ builtin (Sprint 6 P0-10).
func minIn(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	m := data[0]
	for _, v := range data[1:] {
		m = min(m, v)
	}
	return m
}
