package metrics

import (
	"math"

	"github.com/ruoxizhnya/quant-trading/pkg/statistics"
)

// ICCalculator computes Information Coefficient metrics.
type ICCalculator struct{}

// NewICCalculator creates a new ICCalculator.
func NewICCalculator() *ICCalculator {
	return &ICCalculator{}
}

// ICResult holds the result of IC calculation.
type ICResult struct {
	IC           float64   `json:"ic"`
	RankIC       float64   `json:"rank_ic"`
	ICMean       float64   `json:"ic_mean"`
	ICStd        float64   `json:"ic_std"`
	IR           float64   `json:"ir"`
	ICWinRate    float64   `json:"ic_win_rate"`
	ICTimeseries []float64 `json:"ic_timeseries"`
}

// ComputeIC calculates the Pearson correlation between factor values and forward returns.
func (c *ICCalculator) ComputeIC(factorValues, forwardReturns []float64) *ICResult {
	if len(factorValues) == 0 || len(factorValues) != len(forwardReturns) {
		return &ICResult{}
	}

	// Remove NaN pairs
	var cleanF, cleanR []float64
	for i := range factorValues {
		if !math.IsNaN(factorValues[i]) && !math.IsNaN(forwardReturns[i]) {
			cleanF = append(cleanF, factorValues[i])
			cleanR = append(cleanR, forwardReturns[i])
		}
	}

	if len(cleanF) < 2 {
		return &ICResult{}
	}

	ic := statistics.Pearson(cleanF, cleanR)
	rankIC := statistics.Spearman(cleanF, cleanR)

	return &ICResult{
		IC:     ic,
		RankIC: rankIC,
	}
}

// ComputeICTimeseries calculates IC for each time period.
func (c *ICCalculator) ComputeICTimeseries(factorValuesByDate, forwardReturnsByDate map[string][]float64) *ICResult {
	var icSeries []float64
	var validDates []string

	for date := range factorValuesByDate {
		if _, ok := forwardReturnsByDate[date]; !ok {
			continue
		}

		fVals := factorValuesByDate[date]
		rVals := forwardReturnsByDate[date]

		if len(fVals) != len(rVals) || len(fVals) < 2 {
			continue
		}

		// Clean NaN
		var cleanF, cleanR []float64
		for i := range fVals {
			if !math.IsNaN(fVals[i]) && !math.IsNaN(rVals[i]) {
				cleanF = append(cleanF, fVals[i])
				cleanR = append(cleanR, rVals[i])
			}
		}

		if len(cleanF) < 2 {
			continue
		}

		ic := statistics.Pearson(cleanF, cleanR)
		if !math.IsNaN(ic) {
			icSeries = append(icSeries, ic)
			validDates = append(validDates, date)
		}
	}

	if len(icSeries) == 0 {
		return &ICResult{}
	}

	icMean := statistics.Mean(icSeries)
	icStd := statistics.SampleStdDev(icSeries)
	ir := 0.0
	if icStd > 0 {
		ir = icMean / icStd
	}

	winRate := 0.0
	wins := 0
	for _, ic := range icSeries {
		if ic > 0 {
			wins++
		}
	}
	winRate = float64(wins) / float64(len(icSeries))

	return &ICResult{
		IC:           icMean,
		ICMean:       icMean,
		ICStd:        icStd,
		IR:           ir,
		ICWinRate:    winRate,
		ICTimeseries: icSeries,
	}
}

// ComputeIR calculates the Information Ratio (IC mean / IC std).
func (c *ICCalculator) ComputeIR(icSeries []float64) float64 {
	if len(icSeries) == 0 {
		return 0
	}
	mean := statistics.Mean(icSeries)
	std := statistics.SampleStdDev(icSeries)
	// Use small epsilon to handle floating point precision issues
	if std < 1e-10 {
		return 0
	}
	return mean / std
}

// pearsonCorrelation, spearmanCorrelation, rank, mean, and stdDev
// were moved to pkg/statistics (Pearson / Spearman / Rank / Mean /
// SampleStdDev) in ODR-013 P1-21. The two call sites below now use
// the package directly.
