package drift

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/statistics"
)

// Detector performs concept drift detection on strategy performance metrics.
type Detector struct {
	windowSize int
	threshold  float64
	minSamples int
}

// NewDetector creates a new drift detector.
//
// ⚠️ threshold 是 **p 值阈值（显著性水平），不是统计量阈值** —— 三个检测
// 方法的判定统一为 `pValue < threshold`。典型取值 0.05 或 0.01。
//
// 这一点极易搞错，而且**曾经确实不一致**（2026-09-18 修正，P2-6）：
// mean shift 比 p 值，variance shift 比 `logF > threshold*2`，distribution
// shift 比 `ksStat > threshold` —— 三个方法各用一套语义，于是**没有任何
// threshold 取值能同时让三者合理**：传 2.0 会让 mean shift 把一切都判成
// 漂移而 distribution shift 永不触发（KS 统计量最大为 1）；传 0.05 则
// 后两者宽松到形同虚设。现在统一为 p 值，0.05 就是 5% 显著性。
//
// 各方法的原始统计量仍保留在 DriftResult.Statistic 里，供诊断。
func NewDetector(windowSize int, threshold float64) *Detector {
	return &Detector{
		windowSize: windowSize,
		threshold:  threshold,
		minSamples: windowSize * 2,
	}
}

// DriftResult holds the result of drift detection.
type DriftResult struct {
	DriftDetected bool      `json:"drift_detected"`
	DriftType     string    `json:"drift_type,omitempty"`
	Severity      string    `json:"severity,omitempty"`
	PValue        float64   `json:"p_value"`
	Statistic     float64   `json:"statistic"`
	ReferenceMean float64   `json:"reference_mean"`
	CurrentMean   float64   `json:"current_mean"`
	ReferenceStd  float64   `json:"reference_std"`
	CurrentStd    float64   `json:"current_std"`
	Timestamp     time.Time `json:"timestamp"`
	Message       string    `json:"message,omitempty"`
}

// DetectMeanShift detects drift using a two-sample t-test approach.
func (d *Detector) DetectMeanShift(values []float64) (*DriftResult, error) {
	if len(values) < d.minSamples {
		return &DriftResult{
			DriftDetected: false,
			Message:       fmt.Sprintf("insufficient samples: %d < %d", len(values), d.minSamples),
			Timestamp:     time.Now(),
		}, nil
	}

	// Split into reference and current windows
	mid := len(values) - d.windowSize
	if mid < d.windowSize {
		mid = d.windowSize
	}

	reference := values[:mid]
	current := values[mid:]

	refMean, refStd := calculateStats(reference)
	curMean, curStd := calculateStats(current)

	// Calculate t-statistic
	pooledStd := math.Sqrt((refStd*refStd + curStd*curStd) / 2)
	if pooledStd == 0 {
		pooledStd = 1e-10
	}

	n1, n2 := float64(len(reference)), float64(len(current))
	se := pooledStd * math.Sqrt(1.0/n1+1.0/n2)
	tStat := math.Abs(curMean-refMean) / se

	// Approximate p-value (simplified)
	df := n1 + n2 - 2
	pValue := approximatePValue(tStat, df)

	driftDetected := pValue < d.threshold

	severity := "low"
	if driftDetected {
		if pValue < d.threshold/10 {
			severity = "high"
		} else if pValue < d.threshold/3 {
			severity = "medium"
		}
	}

	driftType := "none"
	if driftDetected {
		if curMean < refMean {
			driftType = "degradation"
		} else {
			driftType = "improvement"
		}
	}

	return &DriftResult{
		DriftDetected: driftDetected,
		DriftType:     driftType,
		Severity:      severity,
		PValue:        pValue,
		Statistic:     tStat,
		ReferenceMean: refMean,
		CurrentMean:   curMean,
		ReferenceStd:  refStd,
		CurrentStd:    curStd,
		Timestamp:     time.Now(),
		Message:       fmt.Sprintf("mean shift: %.4f -> %.4f", refMean, curMean),
	}, nil
}

// DetectVarianceShift detects changes in variance using F-test approach.
func (d *Detector) DetectVarianceShift(values []float64) (*DriftResult, error) {
	if len(values) < d.minSamples {
		return &DriftResult{
			DriftDetected: false,
			Message:       fmt.Sprintf("insufficient samples: %d < %d", len(values), d.minSamples),
			Timestamp:     time.Now(),
		}, nil
	}

	mid := len(values) - d.windowSize
	if mid < d.windowSize {
		mid = d.windowSize
	}

	reference := values[:mid]
	current := values[mid:]

	_, refStd := calculateStats(reference)
	_, curStd := calculateStats(current)

	// F-statistic
	var fStat float64
	if refStd > 0 {
		fStat = (curStd * curStd) / (refStd * refStd)
	} else {
		fStat = 1.0
	}

	// Use log to make it symmetric
	logF := math.Abs(math.Log(fStat))

	// 判定统一用 p 值（与均值漂移一致），不再直接比统计量 —— 三个检测
	// 方法曾各用一套语义，导致没有任何 threshold 取值能同时合理。
	pValue := math.Exp(-logF)
	driftDetected := pValue < d.threshold

	severity := "low"
	if driftDetected {
		if pValue < d.threshold/10 {
			severity = "high"
		} else if pValue < d.threshold/2 {
			severity = "medium"
		}
	}

	return &DriftResult{
		DriftDetected: driftDetected,
		DriftType:     "variance_change",
		Severity:      severity,
		PValue:        pValue,
		Statistic:     fStat,
		ReferenceStd:  refStd,
		CurrentStd:    curStd,
		Timestamp:     time.Now(),
		Message:       fmt.Sprintf("variance shift: %.4f -> %.4f", refStd, curStd),
	}, nil
}

// DetectDistributionShift detects drift using Kolmogorov-Smirnov-like statistic.
func (d *Detector) DetectDistributionShift(values []float64) (*DriftResult, error) {
	if len(values) < d.minSamples {
		return &DriftResult{
			DriftDetected: false,
			Message:       fmt.Sprintf("insufficient samples: %d < %d", len(values), d.minSamples),
			Timestamp:     time.Now(),
		}, nil
	}

	mid := len(values) - d.windowSize
	if mid < d.windowSize {
		mid = d.windowSize
	}

	reference := values[:mid]
	current := values[mid:]

	// Calculate empirical CDFs and find maximum difference
	ksStat := calculateKSStatistic(reference, current)

	// 判定统一用 p 值（KS 的渐近近似），与另外两个检测方法一致。
	pValue := math.Exp(-ksStat * ksStat * float64(len(values)) / 2)
	driftDetected := pValue < d.threshold

	severity := "low"
	if driftDetected {
		if pValue < d.threshold/10 {
			severity = "high"
		} else if pValue < d.threshold/2 {
			severity = "medium"
		}
	}

	refMean, _ := calculateStats(reference)
	curMean, _ := calculateStats(current)

	return &DriftResult{
		DriftDetected: driftDetected,
		DriftType:     "distribution_shift",
		Severity:      severity,
		PValue:        pValue,
		Statistic:     ksStat,
		ReferenceMean: refMean,
		CurrentMean:   curMean,
		Timestamp:     time.Now(),
		Message:       fmt.Sprintf("KS statistic: %.4f", ksStat),
	}, nil
}

// DetectAll runs all drift detection methods and returns combined results.
func (d *Detector) DetectAll(values []float64) ([]*DriftResult, error) {
	results := make([]*DriftResult, 0, 3)

	meanResult, err := d.DetectMeanShift(values)
	if err != nil {
		return nil, err
	}
	results = append(results, meanResult)

	varResult, err := d.DetectVarianceShift(values)
	if err != nil {
		return nil, err
	}
	results = append(results, varResult)

	distResult, err := d.DetectDistributionShift(values)
	if err != nil {
		return nil, err
	}
	results = append(results, distResult)

	return results, nil
}

// IsDrifted returns true if any drift is detected.
func IsDrifted(results []*DriftResult) bool {
	for _, r := range results {
		if r.DriftDetected {
			return true
		}
	}
	return false
}

// calculateStats calculates mean and standard deviation.
func calculateStats(values []float64) (mean, std float64) {
	if len(values) == 0 {
		return 0, 0
	}

	mean = statistics.Mean(values)
	std = statistics.PopulationStdDev(values)

	return mean, std
}

// calculateKSStatistic calculates the Kolmogorov-Smirnov statistic.
func calculateKSStatistic(sample1, sample2 []float64) float64 {
	if len(sample1) == 0 || len(sample2) == 0 {
		return 0
	}

	// Sort both samples
	s1 := make([]float64, len(sample1))
	copy(s1, sample1)
	sort.Float64s(s1)

	s2 := make([]float64, len(sample2))
	copy(s2, sample2)
	sort.Float64s(s2)

	// Combine and find unique values
	allValues := make(map[float64]bool)
	for _, v := range s1 {
		allValues[v] = true
	}
	for _, v := range s2 {
		allValues[v] = true
	}

	maxDiff := 0.0
	for v := range allValues {
		cdf1 := empiricalCDF(s1, v)
		cdf2 := empiricalCDF(s2, v)
		diff := math.Abs(cdf1 - cdf2)
		if diff > maxDiff {
			maxDiff = diff
		}
	}

	return maxDiff
}

// empiricalCDF calculates the empirical cumulative distribution function.
func empiricalCDF(sortedData []float64, x float64) float64 {
	if len(sortedData) == 0 {
		return 0
	}

	count := 0
	for _, v := range sortedData {
		if v <= x {
			count++
		}
	}
	return float64(count) / float64(len(sortedData))
}

// approximatePValue approximates the p-value from a t-statistic.
func approximatePValue(tStat, df float64) float64 {
	// Simplified approximation using normal distribution for large df
	if df > 30 {
		return 2 * (1 - normalCDF(tStat))
	}
	// For small df, use a rough approximation
	return 2 * math.Exp(-tStat*tStat/2) / (tStat*math.Sqrt(2*math.Pi) + 1)
}

// normalCDF approximates the standard normal cumulative distribution function.
func normalCDF(x float64) float64 {
	// Abramowitz and Stegun approximation
	b1 := 0.319381530
	b2 := -0.356563782
	b3 := 1.781477937
	b4 := -1.821255978
	b5 := 1.330274429
	p := 0.2316419
	c := 0.39894228

	if x >= 0.0 {
		t := 1.0 / (1.0 + p*x)
		return 1.0 - c*math.Exp(-x*x/2.0)*t*(t*(t*(t*(t*b5+b4)+b3)+b2)+b1)
	}

	t := 1.0 / (1.0 - p*x)
	return c * math.Exp(-x*x/2.0) * t * (t*(t*(t*(t*b5+b4)+b3)+b2) + b1)
}
