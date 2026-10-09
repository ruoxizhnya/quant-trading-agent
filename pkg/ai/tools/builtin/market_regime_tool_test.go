package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════
//  Mock implementations
// ═══════════════════════════════════════════════════════════════════════

// mockRegimeDetector satisfies RegimeDetectorClient for testing.
// It records the OHLCV slice it received and returns a preset regime/error.
type mockRegimeDetector struct {
	regime   *domain.MarketRegime
	err      error
	gotOHLCV []domain.OHLCV
	gotCtx   context.Context
}

func (m *mockRegimeDetector) DetectRegime(ctx context.Context, ohlcv []domain.OHLCV) (*domain.MarketRegime, error) {
	m.gotCtx = ctx
	m.gotOHLCV = ohlcv
	if m.err != nil {
		return nil, m.err
	}
	return m.regime, nil
}

// mockOHLCVFetcher satisfies OHLCVFetcher for testing.
// It returns a preset slice of records/error, and records the args it received.
type mockOHLCVFetcher struct {
	records      []map[string]interface{}
	err          error
	gotSymbol    string
	gotStartDate string
	gotEndDate   string
	gotCtx       context.Context
}

func (m *mockOHLCVFetcher) FetchOHLCV(ctx context.Context, symbol, startDate, endDate string) ([]map[string]interface{}, error) {
	m.gotCtx = ctx
	m.gotSymbol = symbol
	m.gotStartDate = startDate
	m.gotEndDate = endDate
	if m.err != nil {
		return nil, m.err
	}
	return m.records, nil
}

// ─── Test fixtures ───────────────────────────────────────────────────

// makeOHLCVRecords returns n OHLCV records as generic maps, with dates
// from 2024-01-01 forward (one day each) and close prices ascending.
func makeOHLCVRecords(n int) []map[string]interface{} {
	records := make([]map[string]interface{}, n)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		records[i] = map[string]interface{}{
			"symbol":   "000300.SH",
			"date":     base.AddDate(0, 0, i).Format("2006-01-02"),
			"open":     float64(100 + i),
			"high":     float64(101 + i),
			"low":      float64(99 + i),
			"close":    float64(100 + i),
			"volume":   float64(10000 + i*100),
			"turnover": float64(1000000 + i*10000),
		}
	}
	return records
}

// makeOHLCVRecordsUnsorted returns records with dates out of order, to
// verify that recordsToOHLCV sorts them.
func makeOHLCVRecordsUnsorted() []map[string]interface{} {
	return []map[string]interface{}{
		{"symbol": "000300.SH", "date": "2024-01-03", "close": float64(102)},
		{"symbol": "000300.SH", "date": "2024-01-01", "close": float64(100)},
		{"symbol": "000300.SH", "date": "2024-01-02", "close": float64(101)},
	}
}

func newMarketRegimeTool(detector *mockRegimeDetector, fetcher *mockOHLCVFetcher) *GetMarketRegimeTool {
	if detector == nil {
		detector = &mockRegimeDetector{
			regime: &domain.MarketRegime{
				Trend:      "bull",
				Volatility: "medium",
				Sentiment:  0.5,
				Timestamp:  time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
			},
		}
	}
	if fetcher == nil {
		fetcher = &mockOHLCVFetcher{records: makeOHLCVRecords(250)}
	}
	return NewGetMarketRegimeTool(detector, fetcher)
}

// ═══════════════════════════════════════════════════════════════════════
//  Identity tests (Name / Description / Parameters / OutputSchema)
// ═══════════════════════════════════════════════════════════════════════

func TestGetMarketRegimeTool_Name(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	assert.Equal(t, "get_market_regime", tool.Name())
}

func TestGetMarketRegimeTool_Description(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	desc := tool.Description()
	assert.Contains(t, desc, "regime")
	assert.Contains(t, desc, "bull")
	assert.Contains(t, desc, "sideways")
}

func TestGetMarketRegimeTool_Parameters(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	params := tool.Parameters()
	require.Len(t, params, 3)

	// start_date — required
	assert.Equal(t, "start_date", params[0].Name)
	assert.Equal(t, "string", params[0].Type)
	assert.True(t, params[0].Required)

	// end_date — required
	assert.Equal(t, "end_date", params[1].Name)
	assert.Equal(t, "string", params[1].Type)
	assert.True(t, params[1].Required)

	// symbol — optional with default
	assert.Equal(t, "symbol", params[2].Name)
	assert.Equal(t, "string", params[2].Type)
	assert.False(t, params[2].Required)
	assert.Equal(t, DefaultMarketRegimeSymbol, params[2].Default)
}

func TestGetMarketRegimeTool_OutputSchema(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	schema := tool.OutputSchema()
	assert.Equal(t, "object", schema.Type)
	assert.Len(t, schema.Fields, 8)

	fieldNames := make(map[string]bool, len(schema.Fields))
	for _, f := range schema.Fields {
		fieldNames[f.Name] = true
	}
	expectedFields := []string{
		"symbol", "start_date", "end_date", "bars_analyzed",
		"trend", "volatility", "sentiment", "as_of",
	}
	for _, name := range expectedFields {
		assert.True(t, fieldNames[name], "schema should include field %q", name)
	}
}

// ═══════════════════════════════════════════════════════════════════════
//  Constructor nil-panic tests
// ═══════════════════════════════════════════════════════════════════════

func TestNewGetMarketRegimeTool_NilDetectorPanics(t *testing.T) {
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(10)}
	assert.Panics(t, func() {
		NewGetMarketRegimeTool(nil, fetcher)
	})
}

func TestNewGetMarketRegimeTool_NilFetcherPanics(t *testing.T) {
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{}}
	assert.Panics(t, func() {
		NewGetMarketRegimeTool(detector, nil)
	})
}

func TestNewGetMarketRegimeTool_NilBothPanics(t *testing.T) {
	assert.Panics(t, func() {
		NewGetMarketRegimeTool(nil, nil)
	})
}

// ═══════════════════════════════════════════════════════════════════════
//  Execute — happy paths
// ═══════════════════════════════════════════════════════════════════════

func TestGetMarketRegimeTool_Execute_DefaultSymbol(t *testing.T) {
	detector := &mockRegimeDetector{
		regime: &domain.MarketRegime{
			Trend:      "bull",
			Volatility: "low",
			Sentiment:  0.7,
			Timestamp:  time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(250)}

	tool := NewGetMarketRegimeTool(detector, fetcher)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-06-01",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify the fetcher was called with the default symbol.
	assert.Equal(t, DefaultMarketRegimeSymbol, fetcher.gotSymbol)
	assert.Equal(t, "2023-01-01", fetcher.gotStartDate)
	assert.Equal(t, "2024-06-01", fetcher.gotEndDate)

	// Verify the detector received the converted OHLCV bars.
	assert.Len(t, detector.gotOHLCV, 250)

	// Verify the result.
	res, ok := result.(*marketRegimeResult)
	require.True(t, ok)
	assert.Equal(t, DefaultMarketRegimeSymbol, res.Symbol)
	assert.Equal(t, "2023-01-01", res.StartDate)
	assert.Equal(t, "2024-06-01", res.EndDate)
	assert.Equal(t, 250, res.BarsAnalyzed)
	assert.Equal(t, "bull", res.Trend)
	assert.Equal(t, "low", res.Volatility)
	assert.Equal(t, 0.7, res.Sentiment)
	assert.Equal(t, time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), res.AsOf)
}

func TestGetMarketRegimeTool_Execute_CustomSymbol(t *testing.T) {
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{Trend: "bear"}}
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(200)}

	tool := NewGetMarketRegimeTool(detector, fetcher)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-01-01",
		"symbol":     "600000.SH",
	})
	require.NoError(t, err)

	res := result.(*marketRegimeResult)
	assert.Equal(t, "600000.SH", res.Symbol)
	assert.Equal(t, "600000.SH", fetcher.gotSymbol)
	assert.Equal(t, "bear", res.Trend)
}

func TestGetMarketRegimeTool_Execute_BearSidewaysRegimes(t *testing.T) {
	// Verify all three trend values pass through correctly.
	for _, trend := range []string{"bull", "bear", "sideways"} {
		t.Run(trend, func(t *testing.T) {
			detector := &mockRegimeDetector{regime: &domain.MarketRegime{Trend: trend}}
			fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(250)}
			tool := NewGetMarketRegimeTool(detector, fetcher)

			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"start_date": "2023-01-01",
				"end_date":   "2024-01-01",
			})
			require.NoError(t, err)
			assert.Equal(t, trend, result.(*marketRegimeResult).Trend)
		})
	}
}

func TestGetMarketRegimeTool_Execute_VolatilityLevels(t *testing.T) {
	for _, vol := range []string{"low", "medium", "high"} {
		t.Run(vol, func(t *testing.T) {
			detector := &mockRegimeDetector{regime: &domain.MarketRegime{Volatility: vol}}
			fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(250)}
			tool := NewGetMarketRegimeTool(detector, fetcher)

			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"start_date": "2023-01-01",
				"end_date":   "2024-01-01",
			})
			require.NoError(t, err)
			assert.Equal(t, vol, result.(*marketRegimeResult).Volatility)
		})
	}
}

func TestGetMarketRegimeTool_Execute_SentimentRange(t *testing.T) {
	// Sentiment is [-1.0, 1.0] — verify extreme values pass through.
	for _, s := range []float64{-1.0, 0.0, 0.5, 1.0} {
		detector := &mockRegimeDetector{regime: &domain.MarketRegime{Sentiment: s}}
		fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(250)}
		tool := NewGetMarketRegimeTool(detector, fetcher)

		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"start_date": "2023-01-01",
			"end_date":   "2024-01-01",
		})
		require.NoError(t, err)
		assert.Equal(t, s, result.(*marketRegimeResult).Sentiment)
	}
}

// ═══════════════════════════════════════════════════════════════════════
//  Execute — argument validation errors
// ═══════════════════════════════════════════════════════════════════════

func TestGetMarketRegimeTool_Execute_MissingStartDate(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"end_date": "2024-01-01",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestGetMarketRegimeTool_Execute_MissingEndDate(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestGetMarketRegimeTool_Execute_EmptyStartDate(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "",
		"end_date":   "2024-01-01",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestGetMarketRegimeTool_Execute_WrongTypeStartDate(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": 20230101,
		"end_date":   "2024-01-01",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestGetMarketRegimeTool_Execute_WrongTypeSymbol(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-01-01",
		"symbol":     123,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestGetMarketRegimeTool_Execute_InvalidDateFormat(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "not-a-date",
		"end_date":   "2024-01-01",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start_date")
	assert.Contains(t, err.Error(), "YYYY-MM-DD")
}

func TestGetMarketRegimeTool_Execute_InvalidDateFormatEnd(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024/01/01",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "end_date")
	assert.Contains(t, err.Error(), "YYYY-MM-DD")
}

func TestGetMarketRegimeTool_Execute_StartAfterEnd(t *testing.T) {
	tool := newMarketRegimeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2024-06-01",
		"end_date":   "2023-01-01",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be after")
}

func TestGetMarketRegimeTool_Execute_StartEqualsEnd(t *testing.T) {
	// start == end is valid (single day), should not error on date validation.
	// The fetcher may return 0 or 1 records — either is handled.
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{Trend: "sideways"}}
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(1)}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2024-01-01",
		"end_date":   "2024-01-01",
	})
	// Should not error on date validation (fetch/detect may error, but not validation).
	// With 1 record, DetectRegime may fail — but the mock won't.
	assert.NoError(t, err)
}

// ═══════════════════════════════════════════════════════════════════════
//  Execute — fetch errors
// ═══════════════════════════════════════════════════════════════════════

func TestGetMarketRegimeTool_Execute_FetchError(t *testing.T) {
	fetchErr := errors.New("data-service: connection refused")
	fetcher := &mockOHLCVFetcher{err: fetchErr}
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{}}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-01-01",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch OHLCV")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestGetMarketRegimeTool_Execute_EmptyOHLCV(t *testing.T) {
	fetcher := &mockOHLCVFetcher{records: []map[string]interface{}{}}
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{}}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-01-01",
		"symbol":     "999999.SH",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no OHLCV data")
	assert.Contains(t, err.Error(), "999999.SH")
}

func TestGetMarketRegimeTool_Execute_NilOHLCV(t *testing.T) {
	// fetcher returning nil slice should be treated as empty.
	fetcher := &mockOHLCVFetcher{records: nil}
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{}}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-01-01",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no OHLCV data")
}

// ═══════════════════════════════════════════════════════════════════════
//  Execute — DetectRegime errors
// ═══════════════════════════════════════════════════════════════════════

func TestGetMarketRegimeTool_Execute_DetectError(t *testing.T) {
	// Simulate the "insufficient bars" error DetectRegime returns when
	// len(ohlcv) < SlowMAPeriod.
	detectErr := errors.New("insufficient data: need 200 bars, got 50")
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(50)}
	detector := &mockRegimeDetector{err: detectErr}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-01-01",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detect regime")
	assert.Contains(t, err.Error(), "insufficient data")
}

// ═══════════════════════════════════════════════════════════════════════
//  recordsToOHLCV — conversion tests
// ═══════════════════════════════════════════════════════════════════════

func TestRecordsToOHLCV_BasicConversion(t *testing.T) {
	records := []map[string]interface{}{
		{
			"symbol":   "000300.SH",
			"date":     "2024-01-01",
			"open":     float64(100),
			"high":     float64(101),
			"low":      float64(99),
			"close":    float64(100.5),
			"volume":   float64(10000),
			"turnover": float64(1000000),
		},
	}
	bars := recordsToOHLCV(records, "DEFAULT")
	require.Len(t, bars, 1)
	assert.Equal(t, "000300.SH", bars[0].Symbol)
	assert.Equal(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), bars[0].Date)
	assert.Equal(t, 100.0, bars[0].Open)
	assert.Equal(t, 101.0, bars[0].High)
	assert.Equal(t, 99.0, bars[0].Low)
	assert.Equal(t, 100.5, bars[0].Close)
	assert.Equal(t, 10000.0, bars[0].Volume)
	assert.Equal(t, 1000000.0, bars[0].Turnover)
}

func TestRecordsToOHLCV_SortsByDateAscending(t *testing.T) {
	records := makeOHLCVRecordsUnsorted()
	bars := recordsToOHLCV(records, "000300.SH")
	require.Len(t, bars, 3)
	// Verify ascending order.
	assert.Equal(t, "2024-01-01", bars[0].Date.Format("2006-01-02"))
	assert.Equal(t, "2024-01-02", bars[1].Date.Format("2006-01-02"))
	assert.Equal(t, "2024-01-03", bars[2].Date.Format("2006-01-02"))
}

func TestRecordsToOHLCV_MissingSymbolUsesDefault(t *testing.T) {
	records := []map[string]interface{}{
		{"date": "2024-01-01", "close": float64(100)},
		{"symbol": "", "date": "2024-01-02", "close": float64(101)},
	}
	bars := recordsToOHLCV(records, "000300.SH")
	require.Len(t, bars, 2)
	assert.Equal(t, "000300.SH", bars[0].Symbol)
	assert.Equal(t, "000300.SH", bars[1].Symbol)
}

func TestRecordsToOHLCV_MissingCloseIsZero(t *testing.T) {
	records := []map[string]interface{}{
		{"date": "2024-01-01"}, // no close field
	}
	bars := recordsToOHLCV(records, "000300.SH")
	require.Len(t, bars, 1)
	assert.Equal(t, 0.0, bars[0].Close)
}

func TestRecordsToOHLCV_IntCloseValue(t *testing.T) {
	// JSON decoding might produce int instead of float64 in some edge cases.
	records := []map[string]interface{}{
		{"date": "2024-01-01", "close": 100}, // int, not float64
	}
	bars := recordsToOHLCV(records, "000300.SH")
	require.Len(t, bars, 1)
	assert.Equal(t, 100.0, bars[0].Close)
}

func TestRecordsToOHLCV_DateFormats(t *testing.T) {
	// Verify parseFlexibleDate handles multiple date formats.
	records := []map[string]interface{}{
		{"date": "2024-01-01", "close": float64(100)},           // ISO
		{"date": "2024-01-01T00:00:00Z", "close": float64(101)}, // RFC3339
		{"date": "2024-01-01 00:00:00", "close": float64(102)},  // SQL timestamp
		{"date": "2024/01/02", "close": float64(103)},           // Slash-separated
		{"date": "20240103", "close": float64(104)},             // Compact
	}
	bars := recordsToOHLCV(records, "000300.SH")
	require.Len(t, bars, 5)
	// All should parse to valid dates.
	for i, bar := range bars {
		assert.False(t, bar.Date.IsZero(), "bar %d date should not be zero", i)
	}
	// Verify sorting (ascending).
	assert.True(t, bars[0].Date.Before(bars[4].Date))
}

func TestRecordsToOHLCV_UnparseableDateIsZero(t *testing.T) {
	records := []map[string]interface{}{
		{"date": "not-a-date", "close": float64(100)},
	}
	bars := recordsToOHLCV(records, "000300.SH")
	require.Len(t, bars, 1)
	assert.True(t, bars[0].Date.IsZero())
}

func TestRecordsToOHLCV_EmptyInput(t *testing.T) {
	bars := recordsToOHLCV(nil, "000300.SH")
	assert.Len(t, bars, 0)
}

func TestRecordsToOHLCV_LargeInput(t *testing.T) {
	records := makeOHLCVRecords(500)
	bars := recordsToOHLCV(records, "000300.SH")
	require.Len(t, bars, 500)
	// Verify sorted.
	for i := 1; i < len(bars); i++ {
		assert.True(t, bars[i].Date.After(bars[i-1].Date) || bars[i].Date.Equal(bars[i-1].Date),
			"bar %d should be >= bar %d", i, i-1)
	}
}

// ═══════════════════════════════════════════════════════════════════════
//  Helper function tests
// ═══════════════════════════════════════════════════════════════════════

func TestValidateDateFormat_Valid(t *testing.T) {
	assert.NoError(t, validateDateFormat("2024-01-01"))
	assert.NoError(t, validateDateFormat("2024-12-31"))
	assert.NoError(t, validateDateFormat("2023-06-15"))
}

func TestValidateDateFormat_Invalid(t *testing.T) {
	assert.Error(t, validateDateFormat("not-a-date"))
	assert.Error(t, validateDateFormat("2024/01/01"))
	assert.Error(t, validateDateFormat("20240101"))
	assert.Error(t, validateDateFormat("2024-13-01")) // month 13
	assert.Error(t, validateDateFormat("2024-01-32")) // day 32
	assert.Error(t, validateDateFormat(""))
}

func TestToFloat64_Float64(t *testing.T) {
	assert.Equal(t, 3.14, toFloat64(float64(3.14)))
}

func TestToFloat64_Int(t *testing.T) {
	assert.Equal(t, 100.0, toFloat64(100))
}

func TestToFloat64_Int64(t *testing.T) {
	assert.Equal(t, 100.0, toFloat64(int64(100)))
}

func TestToFloat64_Nil(t *testing.T) {
	assert.Equal(t, 0.0, toFloat64(nil))
}

func TestToFloat64_String(t *testing.T) {
	assert.Equal(t, 0.0, toFloat64("100"))
}

func TestParseFlexibleDate_ISO(t *testing.T) {
	d := parseFlexibleDate("2024-01-15")
	assert.Equal(t, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), d)
}

func TestParseFlexibleDate_RFC3339(t *testing.T) {
	d := parseFlexibleDate("2024-01-15T00:00:00Z")
	assert.Equal(t, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), d)
}

func TestParseFlexibleDate_Compact(t *testing.T) {
	d := parseFlexibleDate("20240115")
	assert.Equal(t, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), d)
}

func TestParseFlexibleDate_Invalid(t *testing.T) {
	d := parseFlexibleDate("not-a-date")
	assert.True(t, d.IsZero())
}

// ═══════════════════════════════════════════════════════════════════════
//  Integration tests
// ═══════════════════════════════════════════════════════════════════════

func TestGetMarketRegimeTool_RegisterInRegistry(t *testing.T) {
	reg := tools.NewRegistry()
	tool := newMarketRegimeTool(nil, nil)
	require.NoError(t, reg.Register(tool))

	// Verify it's discoverable.
	listed := reg.List()
	found := false
	for _, t := range listed {
		if t.Name == "get_market_regime" {
			found = true
			break
		}
	}
	assert.True(t, found, "get_market_regime should be in registry")
}

func TestGetMarketRegimeTool_JSONMarshalable(t *testing.T) {
	// Verify the result struct is JSON-marshalable without errors.
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{
		Trend:      "bull",
		Volatility: "high",
		Sentiment:  0.85,
		Timestamp:  time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
	}}
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(250)}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-06-01",
	})
	require.NoError(t, err)

	data, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	// Verify key JSON fields are present.
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Equal(t, "bull", m["trend"])
	assert.Equal(t, "high", m["volatility"])
	assert.Equal(t, "000300.SH", m["symbol"])
	assert.Equal(t, float64(250), m["bars_analyzed"])
}

func TestGetMarketRegimeTool_ContextPropagatedToFetcher(t *testing.T) {
	// Verify the context passed to Execute reaches the fetcher.
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(250)}
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{}}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Execute — fetcher should see a cancelled context

	// We don't assert on error here because the mock fetcher doesn't check ctx.
	// But the mock records it — verify it received the same ctx.
	_, _ = tool.Execute(ctx, map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-01-01",
	})
	assert.Equal(t, ctx, fetcher.gotCtx)
}

func TestGetMarketRegimeTool_ContextPropagatedToDetector(t *testing.T) {
	// Verify the context passed to Execute reaches the detector.
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(250)}
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{}}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	ctx := context.WithValue(context.Background(), "testkey", "testval")
	_, err := tool.Execute(ctx, map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-01-01",
	})
	require.NoError(t, err)
	assert.Equal(t, ctx, detector.gotCtx)
}

// ═══════════════════════════════════════════════════════════════════════
//  DataSourceClient.FetchOHLCV — compile-time interface assertion
// ═══════════════════════════════════════════════════════════════════════

func TestDataSourceClient_SatisfiesOHLCVFetcher(t *testing.T) {
	// Compile-time assertion is at the package level:
	//   var _ OHLCVFetcher = (*DataSourceClient)(nil)
	// This test documents the assertion and ensures it stays in place.
	var _ OHLCVFetcher = (*DataSourceClient)(nil)
	// If this compiles, DataSourceClient.FetchOHLCV has the right signature.
}

// ═══════════════════════════════════════════════════════════════════════
//  Edge cases
// ═══════════════════════════════════════════════════════════════════════

func TestGetMarketRegimeTool_Execute_SingleBar(t *testing.T) {
	// Even 1 bar should pass through (DetectRegime may reject it, but
	// the tool itself shouldn't fail on len==1).
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{Trend: "sideways"}}
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(1)}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2024-01-01",
		"end_date":   "2024-01-01",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.(*marketRegimeResult).BarsAnalyzed)
}

func TestGetMarketRegimeTool_Execute_EmptyStringSymbol(t *testing.T) {
	// Empty string symbol should fall back to default.
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{}}
	fetcher := &mockOHLCVFetcher{records: makeOHLCVRecords(250)}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2023-01-01",
		"end_date":   "2024-01-01",
		"symbol":     "",
	})
	require.NoError(t, err)
	assert.Equal(t, DefaultMarketRegimeSymbol, fetcher.gotSymbol)
}

func TestGetMarketRegimeTool_Execute_SymbolInRecordsOverridesDefault(t *testing.T) {
	// If records have their own symbol, it should be used in the OHLCV bars.
	records := []map[string]interface{}{
		{"symbol": "600000.SH", "date": "2024-01-01", "close": float64(100)},
	}
	detector := &mockRegimeDetector{regime: &domain.MarketRegime{}}
	fetcher := &mockOHLCVFetcher{records: records}
	tool := NewGetMarketRegimeTool(detector, fetcher)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"start_date": "2024-01-01",
		"end_date":   "2024-01-01",
	})
	require.NoError(t, err)
	// The detector should have received bars with the record's symbol.
	assert.Equal(t, "600000.SH", detector.gotOHLCV[0].Symbol)
}
