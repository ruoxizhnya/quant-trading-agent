package drift

import (
	"testing"
	"time"
)

// threshold 的语义是 p 值阈值，不是统计量阈值 —— 这个测试钉住它。
//
// 判定是 `pValue < threshold`。p 值最大为 1，所以任何 >= 1 的 threshold
// 都会让**一切都判成漂移**，包括完全恒定的序列。P2-6 接线时踩过一次
// （传了 2.0），稳定序列被报成漂移才被发现。
func TestThresholdIsPValueNotStatistic(t *testing.T) {
	// 完全平稳、零方差的序列：不该有任何漂移。
	flat := make([]float64, 200)
	for i := range flat {
		flat[i] = 0.01
	}

	sane := NewDetector(60, 0.05)
	results, err := sane.DetectAll(flat)
	if err != nil {
		t.Fatalf("DetectAll: %v", err)
	}
	if IsDrifted(results) {
		t.Error("恒定序列在 threshold=0.05 下不该判成漂移")
	}

	// 反例：把 threshold 当成统计量阈值传 2.0，一切都会"漂移"。
	bogus := NewDetector(60, 2.0)
	results, err = bogus.DetectAll(flat)
	if err != nil {
		t.Fatalf("DetectAll: %v", err)
	}
	if !IsDrifted(results) {
		t.Error("threshold=2.0 应该把一切都判成漂移（这正是它不能这么用的原因）")
	}
}

// 样本不足时必须如实说"判断不了"，而不是返回"没漂移"。
func TestInsufficientSamplesReportsNotDrifted(t *testing.T) {
	short := []float64{0.01, 0.02}
	det := NewDetector(60, 0.05)

	res, err := det.DetectMeanShift(short)
	if err != nil {
		t.Fatalf("DetectMeanShift: %v", err)
	}
	if res.DriftDetected {
		t.Error("样本不足时不该判成漂移")
	}
	if res.Message == "" {
		t.Error("样本不足必须给出说明，不能静默返回「没漂移」")
	}
	if res.Timestamp.IsZero() {
		t.Error("结果应带时间戳")
	}
	_ = time.Now
}
