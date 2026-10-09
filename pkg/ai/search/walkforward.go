package search

import (
	"fmt"
	"math"
)

// WalkForwardValidator performs walk-forward analysis for strategy validation.
type WalkForwardValidator struct {
	trainRatio   float64 // Ratio of data for training
	testRatio    float64 // Ratio of data for testing
	minTrainSize int     // Minimum training window size
	minTestSize  int     // Minimum test window size
}

// NewWalkForwardValidator creates a new walk-forward validator.
func NewWalkForwardValidator() *WalkForwardValidator {
	return &WalkForwardValidator{
		trainRatio:   0.7,
		testRatio:    0.3,
		minTrainSize: 252, // ~1 year of trading days
		minTestSize:  63,  // ~3 months of trading days
	}
}

// WithSizes returns a copy of the validator with the given minimum
// window sizes. The original is not modified (P1-12: validate_l4.go
// needs to tune the window sizes per-call without mutating the
// shared agent's instance).
//
// Values <= 0 are ignored, preserving the existing setting.
func (w *WalkForwardValidator) WithSizes(minTrain, minTest int) *WalkForwardValidator {
	if w == nil {
		w = NewWalkForwardValidator()
	}
	cp := *w
	if minTrain > 0 {
		cp.minTrainSize = minTrain
	}
	if minTest > 0 {
		cp.minTestSize = minTest
	}
	return &cp
}

// WalkForwardResult holds the result of walk-forward analysis.
type WalkForwardResult struct {
	Windows     []WindowResult `json:"windows"`
	ISMetrics   Metrics        `json:"is_metrics"`  // In-sample
	OOSMetrics  Metrics        `json:"oos_metrics"` // Out-of-sample
	Robustness  float64        `json:"robustness"`  // OOS/IS ratio
	OverfitRisk string         `json:"overfit_risk"`
}

// WindowResult holds results for a single walk-forward window.
type WindowResult struct {
	WindowNum   int     `json:"window_num"`
	TrainStart  int     `json:"train_start"`
	TrainEnd    int     `json:"train_end"`
	TestStart   int     `json:"test_start"`
	TestEnd     int     `json:"test_end"`
	TrainReturn float64 `json:"train_return"`
	TestReturn  float64 `json:"test_return"`
	TrainSharpe float64 `json:"train_sharpe"`
	TestSharpe  float64 `json:"test_sharpe"`
}

// Metrics holds performance metrics.
type Metrics struct {
	TotalReturn float64 `json:"total_return"`
	SharpeRatio float64 `json:"sharpe_ratio"`
	MaxDrawdown float64 `json:"max_drawdown"`
	WinRate     float64 `json:"win_rate"`
	Volatility  float64 `json:"volatility"`
}

// Validate runs walk-forward validation on a strategy.
func (w *WalkForwardValidator) Validate(
	dataLength int,
	trainFunc func(trainStart, trainEnd int) Metrics,
	testFunc func(testStart, testEnd int) Metrics,
) (*WalkForwardResult, error) {
	if dataLength < w.minTrainSize+w.minTestSize {
		return nil, fmt.Errorf("insufficient data: %d < %d", dataLength, w.minTrainSize+w.minTestSize)
	}

	result := &WalkForwardResult{
		Windows: make([]WindowResult, 0),
	}

	// Calculate window sizes
	trainSize := int(float64(dataLength) * w.trainRatio)
	testSize := int(float64(dataLength) * w.testRatio)

	if trainSize < w.minTrainSize {
		trainSize = w.minTrainSize
	}
	if testSize < w.minTestSize {
		testSize = w.minTestSize
	}

	// Single walk-forward window
	if trainSize+testSize <= dataLength {
		window := WindowResult{
			WindowNum:  1,
			TrainStart: 0,
			TrainEnd:   trainSize,
			TestStart:  trainSize,
			TestEnd:    trainSize + testSize,
		}

		trainMetrics := trainFunc(window.TrainStart, window.TrainEnd)
		testMetrics := testFunc(window.TestStart, window.TestEnd)

		window.TrainReturn = trainMetrics.TotalReturn
		window.TestReturn = testMetrics.TotalReturn
		window.TrainSharpe = trainMetrics.SharpeRatio
		window.TestSharpe = testMetrics.SharpeRatio

		result.Windows = append(result.Windows, window)

		// Aggregate metrics
		result.ISMetrics = trainMetrics
		result.OOSMetrics = testMetrics
	}

	// Calculate robustness
	if result.ISMetrics.TotalReturn != 0 {
		result.Robustness = result.OOSMetrics.TotalReturn / result.ISMetrics.TotalReturn
	} else {
		result.Robustness = 0
	}

	// Assess overfit risk
	result.OverfitRisk = w.assessOverfitRisk(result.Robustness, result.ISMetrics, result.OOSMetrics)

	return result, nil
}

// ValidateMultipleWindows runs multiple walk-forward windows with step-forward.
func (w *WalkForwardValidator) ValidateMultipleWindows(
	dataLength int,
	stepSize int,
	trainFunc func(trainStart, trainEnd int) Metrics,
	testFunc func(testStart, testEnd int) Metrics,
) (*WalkForwardResult, error) {
	if dataLength < w.minTrainSize+w.minTestSize {
		return nil, fmt.Errorf("insufficient data: %d < %d", dataLength, w.minTrainSize+w.minTestSize)
	}

	result := &WalkForwardResult{
		Windows: make([]WindowResult, 0),
	}

	windowNum := 0
	trainEnd := w.minTrainSize

	for trainEnd+w.minTestSize <= dataLength {
		windowNum++
		trainStart := 0
		if trainEnd > w.minTrainSize {
			trainStart = trainEnd - w.minTrainSize
		}
		testStart := trainEnd
		testEnd := testStart + stepSize
		if testEnd > dataLength {
			testEnd = dataLength
		}

		window := WindowResult{
			WindowNum:  windowNum,
			TrainStart: trainStart,
			TrainEnd:   trainEnd,
			TestStart:  testStart,
			TestEnd:    testEnd,
		}

		trainMetrics := trainFunc(window.TrainStart, window.TrainEnd)
		testMetrics := testFunc(window.TestStart, window.TestEnd)

		window.TrainReturn = trainMetrics.TotalReturn
		window.TestReturn = testMetrics.TotalReturn
		window.TrainSharpe = trainMetrics.SharpeRatio
		window.TestSharpe = testMetrics.SharpeRatio

		result.Windows = append(result.Windows, window)

		trainEnd += stepSize
	}

	// Aggregate metrics across all windows
	if len(result.Windows) > 0 {
		var totalISReturn, totalOOSReturn, totalISSharpe, totalOOSSharpe float64
		for _, w := range result.Windows {
			totalISReturn += w.TrainReturn
			totalOOSReturn += w.TestReturn
			totalISSharpe += w.TrainSharpe
			totalOOSSharpe += w.TestSharpe
		}

		n := float64(len(result.Windows))
		result.ISMetrics = Metrics{
			TotalReturn: totalISReturn / n,
			SharpeRatio: totalISSharpe / n,
		}
		result.OOSMetrics = Metrics{
			TotalReturn: totalOOSReturn / n,
			SharpeRatio: totalOOSSharpe / n,
		}
	}

	// Calculate robustness
	if result.ISMetrics.TotalReturn != 0 {
		result.Robustness = result.OOSMetrics.TotalReturn / result.ISMetrics.TotalReturn
	} else {
		result.Robustness = 0
	}

	result.OverfitRisk = w.assessOverfitRisk(result.Robustness, result.ISMetrics, result.OOSMetrics)

	return result, nil
}

// assessOverfitRisk assesses the overfitting risk based on IS/OOS metrics.
func (w *WalkForwardValidator) assessOverfitRisk(robustness float64, isMetrics, oosMetrics Metrics) string {
	if robustness < 0 {
		return "high" // OOS negative while IS positive
	}
	if robustness < 0.3 {
		return "high"
	}
	if robustness < 0.5 {
		return "medium"
	}
	if robustness < 0.7 {
		return "low"
	}

	// Check Sharpe degradation
	if isMetrics.SharpeRatio > 0 && oosMetrics.SharpeRatio > 0 {
		sharpeRatio := oosMetrics.SharpeRatio / isMetrics.SharpeRatio
		if sharpeRatio < 0.5 {
			return "medium"
		}
	}

	return "low"
}

// CalculateCSCV performs Combinatorially Symmetric Cross-Validation.
func (w *WalkForwardValidator) CalculateCSCV(returns []float64, nSplits int) float64 {
	if len(returns) < nSplits*2 {
		return 0
	}

	splitSize := len(returns) / nSplits
	if splitSize == 0 {
		return 0
	}

	// Calculate log returns
	logReturns := make([]float64, len(returns))
	for i, r := range returns {
		logReturns[i] = math.Log(1 + r)
	}

	// Calculate performance for each split
	splitPerformances := make([]float64, nSplits)
	for i := 0; i < nSplits; i++ {
		start := i * splitSize
		end := start + splitSize
		if end > len(logReturns) {
			end = len(logReturns)
		}

		var sum float64
		for _, r := range logReturns[start:end] {
			sum += r
		}
		splitPerformances[i] = sum
	}

	// Calculate CSCV metric (probability that IS > OOS)
	isBetterCount := 0
	totalComparisons := 0

	for i := 0; i < nSplits; i++ {
		for j := i + 1; j < nSplits; j++ {
			if splitPerformances[i] > splitPerformances[j] {
				isBetterCount++
			}
			totalComparisons++
		}
	}

	if totalComparisons == 0 {
		return 0.5
	}

	return float64(isBetterCount) / float64(totalComparisons)
}
