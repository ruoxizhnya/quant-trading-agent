package factor

import (
	"math"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/data/source"
)

func TestSentimentFactor_HotSearchRank1(t *testing.T) {
	ref := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []HotSearchRow{
		{Symbol: "A", TradeTime: ref, Rank: 1},
		{Symbol: "B", TradeTime: ref, Rank: 20},
		{Symbol: "C", TradeTime: ref, Rank: 100}, // out of top-20 → ignored
	}
	factor := SentimentFactor(rows, ref)
	if factor["A"] < 0.95 {
		t.Errorf("A (rank 1) = %v, want ~1.0", factor["A"])
	}
	if factor["B"] < 0.0 || factor["B"] > 0.1 {
		t.Errorf("B (rank 20) = %v, want ~0.0", factor["B"])
	}
	if _, ok := factor["C"]; ok {
		t.Errorf("C (rank 100) should be ignored, got %v", factor["C"])
	}
}

func TestSentimentFactor_NewsSentimentMapping(t *testing.T) {
	ref := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []HotSearchRow{
		{Symbol: "POS", TradeTime: ref, Title: "stock soars", Sentiment: 1.0},     // bullish → 1.0
		{Symbol: "NEU", TradeTime: ref, Title: "earnings report", Sentiment: 0.0}, // neutral → 0.5
		{Symbol: "NEG", TradeTime: ref, Title: "stock tumbles", Sentiment: -1.0},  // bearish → 0.0
	}
	factor := SentimentFactor(rows, ref)
	if math.Abs(factor["POS"]-1.0) > 1e-9 {
		t.Errorf("POS = %v, want 1.0", factor["POS"])
	}
	if math.Abs(factor["NEU"]-0.5) > 1e-9 {
		t.Errorf("NEU = %v, want 0.5", factor["NEU"])
	}
	if math.Abs(factor["NEG"]-0.0) > 1e-9 {
		t.Errorf("NEG = %v, want 0.0", factor["NEG"])
	}
}

func TestSentimentFactor_TimeDecay(t *testing.T) {
	ref := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	// Each symbol has TWO events: a recent rank-1 hit and a stale
	// rank-20 mention. The proportion of high-rank vs low-rank
	// contributions should be HIGHER for the more recent symbol
	// because the stale event carries less weight.
	rows := []HotSearchRow{
		// OLD: stale rank-1 hit (day -7, low weight) + fresh rank-20
		// mention (day 0, full weight). The fresh-but-low-rank
		// event dominates.
		{Symbol: "OLD", TradeTime: ref.AddDate(0, 0, -7), Rank: 1},
		{Symbol: "OLD", TradeTime: ref, Rank: 20},
		// NEW: fresh rank-1 hit + fresh rank-20 mention. The
		// high-rank event dominates.
		{Symbol: "NEW", TradeTime: ref, Rank: 1},
		{Symbol: "NEW", TradeTime: ref, Rank: 20},
	}
	factor := SentimentFactor(rows, ref)
	if factor["NEW"] <= factor["OLD"] {
		t.Errorf("NEW (%v) should be > OLD (%v); time decay should let the recent high-rank hit dominate", factor["NEW"], factor["OLD"])
	}
}

func TestSentimentFactor_ClipsToUnitInterval(t *testing.T) {
	ref := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	// Pathological input: 100 rank-1 events should still cap at 1.
	rows := make([]HotSearchRow, 100)
	for i := range rows {
		rows[i] = HotSearchRow{Symbol: "X", TradeTime: ref, Rank: 1}
	}
	factor := SentimentFactor(rows, ref)
	if factor["X"] > 1.0 {
		t.Errorf("X = %v, want <= 1.0", factor["X"])
	}
}

func TestSentimentFactor_EmptyInput(t *testing.T) {
	factor := SentimentFactor(nil, time.Now())
	if len(factor) != 0 {
		t.Errorf("len = %d, want 0", len(factor))
	}
}

func TestTopBuzzSymbols(t *testing.T) {
	ref := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []HotSearchRow{
		{Symbol: "A", TradeTime: ref, Rank: 1},
		{Symbol: "B", TradeTime: ref, Rank: 2},
		{Symbol: "C", TradeTime: ref, Rank: 3},
	}
	top := TopBuzzSymbols(rows, ref, 2)
	if len(top) != 2 {
		t.Fatalf("len = %d, want 2", len(top))
	}
	if top[0] != "A" {
		t.Errorf("top[0] = %q, want A", top[0])
	}
	if top[1] != "B" {
		t.Errorf("top[1] = %q, want B", top[1])
	}
}

func TestHotSearchFromPoints(t *testing.T) {
	now := time.Now()
	points := []source.UnifiedDataPoint{
		{
			Symbol:    "A",
			TradeTime: now,
			DataType:  source.DataTypeHotSearch,
			Data:      map[string]interface{}{"rank": float64(5), "keyword": "test", "heat": 100.0},
		},
		{
			Symbol:    "B",
			TradeTime: now,
			DataType:  source.DataTypeNews,
			Data:      map[string]interface{}{"title": "earnings beat", "sentiment": 0.7},
		},
		{
			// Wrong data type should be ignored.
			Symbol:    "C",
			TradeTime: now,
			DataType:  source.DataTypeOHLCDaily,
			Data:      map[string]interface{}{},
		},
	}
	rows := HotSearchFromPoints(points)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
}

func TestIsHotSearchRowValid(t *testing.T) {
	now := time.Now()
	cases := []struct {
		row  HotSearchRow
		want bool
	}{
		{HotSearchRow{Symbol: "A", TradeTime: now, Heat: 1}, true},
		{HotSearchRow{TradeTime: now, Heat: 1}, false}, // no symbol
		{HotSearchRow{Symbol: "A", Heat: 1}, false},    // no time
		{HotSearchRow{Symbol: "A", TradeTime: now, Heat: math.NaN()}, false},
	}
	for i, c := range cases {
		if got := IsHotSearchRowValid(c.row); got != c.want {
			t.Errorf("case %d: got %v, want %v", i, got, c.want)
		}
	}
}

// CR-53 (ODR-012): NaN/Inf tolerance for SentimentFactor.
// The sentiment pipeline ingests upstream scores (xueqiu, LLM
// extraction) that can produce NaN/Inf for the Heat/Sentiment
// fields when the source returns a malformed payload. SentimentFactor
// must not propagate NaN/Inf downstream: the output is later used in
// IC computation, where a single NaN entry would void the entire
// cross-section. The fix has two layers:
//  1. The function's arithmetic must not turn a NaN input into a
//     NaN output (or if it does, the row must be skipped).
//  2. The output range [0, 1] must hold; NaN is by definition
//     neither, so emitting NaN is a contract violation.
func TestSentimentFactor_NaNInfTolerance(t *testing.T) {
	ref := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []HotSearchRow{
		{Symbol: "VALID", TradeTime: ref, Rank: 1}, // baseline
		{Symbol: "NAN_SENT", TradeTime: ref, Title: "weird", Sentiment: math.NaN()},
		{Symbol: "INF_SENT", TradeTime: ref, Title: "weird", Sentiment: math.Inf(1)},
		{Symbol: "NEG_INF", TradeTime: ref, Title: "weird", Sentiment: math.Inf(-1)},
		{Symbol: "NAN_HEAT", TradeTime: ref, Rank: 5, Heat: math.NaN()},
	}
	factor := SentimentFactor(rows, ref)

	// VALID (rank 1) is a known-good input — its score must be the
	// rank-1 = 1.0 value. If the function short-circuits on the NaN
	// inputs and zeroes everything, we'd see ~0.5 here, which would
	// be a regression in the rank-based path.
	v := factor["VALID"]
	if math.IsNaN(v) || math.IsInf(v, 0) {
		t.Errorf("VALID (rank 1) became %v, must not be NaN/Inf", v)
	}
	if v < 0.95 {
		t.Errorf("VALID (rank 1) = %v, want ~1.0; NaN inputs may be polluting the accumulator", v)
	}
	if v > 1.0 {
		t.Errorf("VALID (rank 1) = %v, want ≤1.0; clip-to-unit-interval is broken", v)
	}

	// The NaN/Inf inputs must either produce a clipped [0, 1] value
	// or be absent from the output. They must NEVER be NaN/Inf.
	for _, sym := range []string{"NAN_SENT", "INF_SENT", "NEG_INF", "NAN_HEAT"} {
		out, ok := factor[sym]
		if !ok {
			continue // acceptable: skipped entirely
		}
		if math.IsNaN(out) || math.IsInf(out, 0) {
			t.Errorf("%s (NaN/Inf input) produced %v; would poison downstream IC", sym, out)
		}
		if out < 0 || out > 1 {
			t.Errorf("%s = %v, must be in [0, 1]", sym, out)
		}
	}
}

func TestSentimentFactor_AllNaNInput(t *testing.T) {
	// Edge case: ALL rows have NaN sentiment. The function must
	// not return a map of NaN values; it should either return an
	// empty map (preferred) or zero-fill, but never NaN.
	ref := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	rows := []HotSearchRow{
		{Symbol: "A", TradeTime: ref, Title: "x", Sentiment: math.NaN()},
		{Symbol: "B", TradeTime: ref, Title: "y", Sentiment: math.NaN()},
	}
	factor := SentimentFactor(rows, ref)
	for sym, v := range factor {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("symbol %s = %v on all-NaN input — must be skipped or zeroed", sym, v)
		}
	}
}
