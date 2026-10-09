package factor

import (
	"math"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/data/source"
)

func TestCapitalFlowFromPoints(t *testing.T) {
	now := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	points := []source.UnifiedDataPoint{
		{
			Symbol:    "600519.SH",
			TradeTime: now,
			DataType:  source.DataTypeCapitalFlow,
			Data: map[string]interface{}{
				"period":      "5d",
				"main_net":    1.5e8,
				"super_net":   8e7,
				"close_price": 1700.0,
			},
		},
		{
			// Wrong data type should be ignored.
			Symbol:    "999999",
			TradeTime: now,
			DataType:  source.DataTypeOHLCDaily,
			Data:      map[string]interface{}{},
		},
	}
	rows := CapitalFlowFromPoints(points)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Symbol != "600519.SH" {
		t.Errorf("Symbol = %q, want 600519.SH", r.Symbol)
	}
	if r.Period != "5d" {
		t.Errorf("Period = %q, want 5d", r.Period)
	}
	if r.MainNet != 1.5e8 {
		t.Errorf("MainNet = %v, want 1.5e8", r.MainNet)
	}
	if r.ClosePrice != 1700.0 {
		t.Errorf("ClosePrice = %v, want 1700.0", r.ClosePrice)
	}
}

func TestCapitalFlowFactor_PositiveAccumulation(t *testing.T) {
	now := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []CapitalFlowRow{
		{Symbol: "A", TradeTime: now, MainNet: 1e6, ClosePrice: 100},
		{Symbol: "A", TradeTime: now.AddDate(0, 0, -1), MainNet: 1e6, ClosePrice: 100},
		{Symbol: "B", TradeTime: now, MainNet: -1e6, ClosePrice: 100},
		{Symbol: "B", TradeTime: now.AddDate(0, 0, -1), MainNet: -1e6, ClosePrice: 100},
	}
	factor := CapitalFlowFactor(rows, 5)
	if factor["A"] != 2e6/100 {
		t.Errorf("A factor = %v, want %v", factor["A"], 2e6/100)
	}
	if factor["B"] != -2e6/100 {
		t.Errorf("B factor = %v, want %v", factor["B"], -2e6/100)
	}
	// A should be higher than B (positive > negative).
	if factor["A"] <= factor["B"] {
		t.Errorf("A (%v) should be > B (%v)", factor["A"], factor["B"])
	}
}

func TestCapitalFlowFactor_DefaultsLookback(t *testing.T) {
	now := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []CapitalFlowRow{
		{Symbol: "A", TradeTime: now, MainNet: 1e6, ClosePrice: 100},
	}
	// lookback <= 0 falls back to 5.
	factor := CapitalFlowFactor(rows, 0)
	if _, ok := factor["A"]; !ok {
		t.Errorf("expected A in factor output, got %v", factor)
	}
}

func TestCapitalFlowFactor_RespectsLookback(t *testing.T) {
	now := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []CapitalFlowRow{
		{Symbol: "A", TradeTime: now, MainNet: 1e8, ClosePrice: 100},                   // day 0
		{Symbol: "A", TradeTime: now.AddDate(0, 0, -1), MainNet: 1e8, ClosePrice: 100}, // day -1
		{Symbol: "A", TradeTime: now.AddDate(0, 0, -2), MainNet: 1e8, ClosePrice: 100}, // day -2
		{Symbol: "A", TradeTime: now.AddDate(0, 0, -3), MainNet: 1e8, ClosePrice: 100}, // day -3
		{Symbol: "A", TradeTime: now.AddDate(0, 0, -4), MainNet: 1e8, ClosePrice: 100}, // day -4
		{Symbol: "A", TradeTime: now.AddDate(0, 0, -5), MainNet: 1e8, ClosePrice: 100}, // day -5 (excluded for lookback=5)
	}
	factor := CapitalFlowFactor(rows, 5)
	want := 5 * 1e8 / 100
	if factor["A"] != want {
		t.Errorf("A factor = %v, want %v (lookback=5 should sum 5 days)", factor["A"], want)
	}
}

func TestCapitalFlowFactor_SkipsZeroClose(t *testing.T) {
	rows := []CapitalFlowRow{
		{Symbol: "A", TradeTime: time.Now(), MainNet: 1e6, ClosePrice: 0}, // no price → skip
	}
	factor := CapitalFlowFactor(rows, 5)
	if _, ok := factor["A"]; ok {
		t.Errorf("A with ClosePrice=0 should be skipped, got %v", factor["A"])
	}
}

// CR-42 (ODR-012): pins the suspended-day (停牌) semantics documented
// on CapitalFlowFactor. A 3-day window with the most recent day being
// a suspension (close=0) must drop the symbol entirely via the
// closeRef guard. A window where the suspension is older than the
// first row in the slice must just be summed across the trading days
// (no calendar-based gap-fill).
func TestCapitalFlowFactor_SuspendedDaySemantics(t *testing.T) {
	now := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)

	t.Run("suspended as most recent day → symbol dropped", func(t *testing.T) {
		rows := []CapitalFlowRow{
			{Symbol: "A", TradeTime: now, MainNet: 0, ClosePrice: 0}, // 停牌 (most recent)
			{Symbol: "A", TradeTime: now.AddDate(0, 0, -1), MainNet: 2e6, ClosePrice: 100},
			{Symbol: "A", TradeTime: now.AddDate(0, 0, -2), MainNet: 3e6, ClosePrice: 100},
		}
		factor := CapitalFlowFactor(rows, 3)
		if _, ok := factor["A"]; ok {
			t.Errorf("expected A to be dropped when most recent day is suspended, got %v", factor["A"])
		}
	})

	t.Run("suspended day inside window (not most recent) → summed as zero, factor survives", func(t *testing.T) {
		rows := []CapitalFlowRow{
			{Symbol: "A", TradeTime: now, MainNet: 5e6, ClosePrice: 100},                 // day 0
			{Symbol: "A", TradeTime: now.AddDate(0, 0, -1), MainNet: 0, ClosePrice: 100}, // 停牌 day -1
			{Symbol: "A", TradeTime: now.AddDate(0, 0, -2), MainNet: 2e6, ClosePrice: 100},
		}
		factor := CapitalFlowFactor(rows, 3)
		want := 7e6 / 100 // 5 + 0 + 2 = 7
		if factor["A"] != want {
			t.Errorf("A factor = %v, want %v (suspended day should sum as zero, not skip)", factor["A"], want)
		}
	})

	t.Run("upstream omits suspended days → window is just the trading days that exist", func(t *testing.T) {
		// 5 days would normally be returned; upstream dropped 2 of them
		// (模拟停牌). Factor must use whatever rows remain, no gap-fill.
		rows := []CapitalFlowRow{
			{Symbol: "A", TradeTime: now, MainNet: 1e6, ClosePrice: 100},
			{Symbol: "A", TradeTime: now.AddDate(0, 0, -1), MainNet: 1e6, ClosePrice: 100},
			{Symbol: "A", TradeTime: now.AddDate(0, 0, -4), MainNet: 1e6, ClosePrice: 100},
		}
		factor := CapitalFlowFactor(rows, 5)
		want := 3e6 / 100 // just 3 actual rows summed
		if factor["A"] != want {
			t.Errorf("A factor = %v, want %v (no calendar gap-fill expected)", factor["A"], want)
		}
	})
}

func TestCapitalFlowICSign(t *testing.T) {
	now := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []CapitalFlowRow{
		{Symbol: "A", TradeTime: now.AddDate(0, 0, -1), MainNet: -1e6},
		{Symbol: "A", TradeTime: now, MainNet: 5e6}, // latest wins
		{Symbol: "B", TradeTime: now, MainNet: -2e6},
		{Symbol: "C", TradeTime: now, MainNet: 0},
	}
	if got := CapitalFlowICSign(rows, "A"); got != 1 {
		t.Errorf("A sign = %d, want 1", got)
	}
	if got := CapitalFlowICSign(rows, "B"); got != -1 {
		t.Errorf("B sign = %d, want -1", got)
	}
	if got := CapitalFlowICSign(rows, "C"); got != 0 {
		t.Errorf("C sign = %d, want 0", got)
	}
	if got := CapitalFlowICSign(rows, "ZZZ"); got != 0 {
		t.Errorf("ZZZ sign = %d, want 0", got)
	}
}

func TestIsCapitalFlowRowValid(t *testing.T) {
	now := time.Now()
	cases := []struct {
		row  CapitalFlowRow
		want bool
	}{
		{CapitalFlowRow{Symbol: "A", TradeTime: now, MainNet: 1}, true},
		{CapitalFlowRow{TradeTime: now, MainNet: 1}, false},                        // no symbol
		{CapitalFlowRow{Symbol: "A", MainNet: 1}, false},                           // no time
		{CapitalFlowRow{Symbol: "A", TradeTime: now, MainNet: math.NaN()}, false},  // NaN
		{CapitalFlowRow{Symbol: "A", TradeTime: now, MainNet: math.Inf(1)}, false}, // +Inf
	}
	for i, c := range cases {
		if got := IsCapitalFlowRowValid(c.row); got != c.want {
			t.Errorf("case %d: got %v, want %v", i, got, c.want)
		}
	}
}
