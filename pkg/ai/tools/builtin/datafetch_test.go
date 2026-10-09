package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
)

// ─── mock data-service ────────────────────────────────────────────────

// dataTestServer spins up an httptest.Server that mimics the
// data-service's /ohlcv, /stocks, and /fundamentals endpoints.
type dataTestServer struct {
	server   *httptest.Server
	lastPath string
	failWith string
}

func newDataTestServer() *dataTestServer {
	s := &dataTestServer{}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *dataTestServer) handle(w http.ResponseWriter, r *http.Request) {
	s.lastPath = r.URL.Path + "?" + r.URL.RawQuery

	if s.failWith != "" {
		http.Error(w, s.failWith, 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.URL.Path == "/ohlcv/000001.SZ":
		// data-service wraps OHLCV in {"ohlcv": [...]}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ohlcv": []map[string]interface{}{
				{"symbol": "000001.SZ", "date": "2022-01-04", "open": 10.5, "close": 10.8},
				{"symbol": "000001.SZ", "date": "2022-01-05", "open": 10.8, "close": 11.0},
			},
		})

	case r.URL.Path == "/stocks":
		json.NewEncoder(w).Encode(map[string]interface{}{
			"stocks": []map[string]interface{}{
				{"symbol": "000001.SZ", "name": "平安银行"},
				{"symbol": "600000.SH", "name": "浦发银行"},
			},
		})

	case r.URL.Path == "/stocks/000001.SZ":
		json.NewEncoder(w).Encode(map[string]interface{}{
			"stock": map[string]interface{}{
				"symbol": "000001.SZ", "name": "平安银行", "exchange": "SZ",
			},
		})

	case r.URL.Path == "/fundamentals/000001.SZ":
		// Without date range, return a single object.
		if r.URL.Query().Get("start_date") == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"fundamentals": map[string]interface{}{
					"symbol": "000001.SZ", "pe": 8.5, "pb": 0.9,
				},
			})
		} else {
			// With date range, return an array.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"fundamentals": []map[string]interface{}{
					{"symbol": "000001.SZ", "date": "2022-03-31", "pe": 8.5},
					{"symbol": "000001.SZ", "date": "2022-06-30", "pe": 8.2},
				},
			})
		}

	default:
		http.Error(w, "not found", 404)
	}
}

func (s *dataTestServer) close() { s.server.Close() }

// ─── DataOHLCVTool tests ──────────────────────────────────────────────

func TestDataOHLCVTool_Name(t *testing.T) {
	c := NewDataSourceClient("http://x", nil)
	tt := NewDataOHLCVTool(c)
	assert.Equal(t, "data.ohlcv", tt.Name())
}

func TestDataOHLCVTool_Parameters(t *testing.T) {
	c := NewDataSourceClient("http://x", nil)
	tt := NewDataOHLCVTool(c)
	params := tt.Parameters()
	require.Len(t, params, 3)
	for _, p := range params {
		assert.True(t, p.Required)
	}
}

func TestDataOHLCVTool_OutputSchema(t *testing.T) {
	c := NewDataSourceClient("http://x", nil)
	tt := NewDataOHLCVTool(c)
	schema := tt.OutputSchema()
	assert.Equal(t, "array", schema.Type)
	assert.NotEmpty(t, schema.Fields)
}

func TestDataOHLCVTool_Execute_HappyPath(t *testing.T) {
	srv := newDataTestServer()
	defer srv.close()

	c := NewDataSourceClient(srv.server.URL, nil)
	tt := NewDataOHLCVTool(c)

	args := map[string]interface{}{
		"symbol":     "000001.SZ",
		"start_date": "2022-01-01",
		"end_date":   "2022-01-31",
	}

	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)

	out, ok := result.([]map[string]interface{})
	require.True(t, ok, "result should be []map[string]interface{}, got %T", result)
	require.Len(t, out, 2)
	assert.Equal(t, "000001.SZ", out[0]["symbol"])

	// Verify the date was converted to YYYYMMDD in the request path.
	assert.Contains(t, srv.lastPath, "start_date=20220101")
	assert.Contains(t, srv.lastPath, "end_date=20220131")
}

func TestDataOHLCVTool_Execute_MissingSymbol(t *testing.T) {
	srv := newDataTestServer()
	defer srv.close()

	tt := NewDataOHLCVTool(NewDataSourceClient(srv.server.URL, nil))
	args := map[string]interface{}{
		"start_date": "2022-01-01",
		"end_date":   "2022-01-31",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "symbol")
}

func TestDataOHLCVTool_Execute_MissingStartDate(t *testing.T) {
	srv := newDataTestServer()
	defer srv.close()

	tt := NewDataOHLCVTool(NewDataSourceClient(srv.server.URL, nil))
	args := map[string]interface{}{
		"symbol":   "000001.SZ",
		"end_date": "2022-01-31",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "start_date")
}

func TestDataOHLCVTool_Execute_DownstreamError(t *testing.T) {
	srv := newDataTestServer()
	srv.failWith = "data-service down"
	defer srv.close()

	tt := NewDataOHLCVTool(NewDataSourceClient(srv.server.URL, nil))
	args := map[string]interface{}{
		"symbol":     "000001.SZ",
		"start_date": "2022-01-01",
		"end_date":   "2022-01-31",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.False(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "data.ohlcv")
}

func TestNewDataOHLCVTool_NilClientPanics(t *testing.T) {
	assert.Panics(t, func() { NewDataOHLCVTool(nil) })
}

// ─── DataStocksTool tests ─────────────────────────────────────────────

func TestDataStocksTool_Name(t *testing.T) {
	tt := NewDataStocksTool(NewDataSourceClient("http://x", nil))
	assert.Equal(t, "data.stocks", tt.Name())
}

func TestDataStocksTool_Parameters(t *testing.T) {
	tt := NewDataStocksTool(NewDataSourceClient("http://x", nil))
	params := tt.Parameters()
	require.Len(t, params, 1)
	assert.False(t, params[0].Required, "symbol should be optional for data.stocks")
}

func TestDataStocksTool_Execute_AllStocks(t *testing.T) {
	srv := newDataTestServer()
	defer srv.close()

	tt := NewDataStocksTool(NewDataSourceClient(srv.server.URL, nil))
	result, err := tt.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)

	out, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, out, 2)
	assert.Equal(t, "000001.SZ", out[0]["symbol"])
}

func TestDataStocksTool_Execute_SingleStock(t *testing.T) {
	srv := newDataTestServer()
	defer srv.close()

	tt := NewDataStocksTool(NewDataSourceClient(srv.server.URL, nil))
	args := map[string]interface{}{"symbol": "000001.SZ"}
	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)

	out, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, out, 1, "single-stock response should be wrapped in a slice")
	assert.Equal(t, "000001.SZ", out[0]["symbol"])
}

func TestDataStocksTool_Execute_DownstreamError(t *testing.T) {
	srv := newDataTestServer()
	srv.failWith = "db down"
	defer srv.close()

	tt := NewDataStocksTool(NewDataSourceClient(srv.server.URL, nil))
	_, err := tt.Execute(context.Background(), map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data.stocks")
}

func TestNewDataStocksTool_NilClientPanics(t *testing.T) {
	assert.Panics(t, func() { NewDataStocksTool(nil) })
}

// ─── DataFundamentalsTool tests ───────────────────────────────────────

func TestDataFundamentalsTool_Name(t *testing.T) {
	tt := NewDataFundamentalsTool(NewDataSourceClient("http://x", nil))
	assert.Equal(t, "data.fundamentals", tt.Name())
}

func TestDataFundamentalsTool_Parameters(t *testing.T) {
	tt := NewDataFundamentalsTool(NewDataSourceClient("http://x", nil))
	params := tt.Parameters()
	require.Len(t, params, 3)
	// symbol is required; dates are optional.
	assert.True(t, params[0].Required)
	assert.False(t, params[1].Required)
	assert.False(t, params[2].Required)
}

func TestDataFundamentalsTool_Execute_NoDateRange(t *testing.T) {
	srv := newDataTestServer()
	defer srv.close()

	tt := NewDataFundamentalsTool(NewDataSourceClient(srv.server.URL, nil))
	args := map[string]interface{}{"symbol": "000001.SZ"}
	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)

	out, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, out, 1, "single snapshot should be wrapped in a slice")
	assert.Equal(t, "000001.SZ", out[0]["symbol"])
}

func TestDataFundamentalsTool_Execute_WithDateRange(t *testing.T) {
	srv := newDataTestServer()
	defer srv.close()

	tt := NewDataFundamentalsTool(NewDataSourceClient(srv.server.URL, nil))
	args := map[string]interface{}{
		"symbol":     "000001.SZ",
		"start_date": "2022-01-01",
		"end_date":   "2022-12-31",
	}
	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)

	out, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, out, 2)
}

func TestDataFundamentalsTool_Execute_MissingSymbol(t *testing.T) {
	srv := newDataTestServer()
	defer srv.close()

	tt := NewDataFundamentalsTool(NewDataSourceClient(srv.server.URL, nil))
	_, err := tt.Execute(context.Background(), map[string]interface{}{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "symbol")
}

func TestDataFundamentalsTool_Execute_DownstreamError(t *testing.T) {
	srv := newDataTestServer()
	srv.failWith = "db error"
	defer srv.close()

	tt := NewDataFundamentalsTool(NewDataSourceClient(srv.server.URL, nil))
	args := map[string]interface{}{"symbol": "000001.SZ"}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data.fundamentals")
}

func TestNewDataFundamentalsTool_NilClientPanics(t *testing.T) {
	assert.Panics(t, func() { NewDataFundamentalsTool(nil) })
}

// ─── toYYYYMMDD helper ────────────────────────────────────────────────

func TestToYYYYMMDD(t *testing.T) {
	assert.Equal(t, "20220101", toYYYYMMDD("2022-01-01"))
	assert.Equal(t, "20241231", toYYYYMMDD("2024-12-31"))
	// Invalid input passes through unchanged (downstream 400 surfaces the error).
	assert.Equal(t, "not-a-date", toYYYYMMDD("not-a-date"))
}

// ─── Registry integration ─────────────────────────────────────────────

func TestDataTools_RegisterInRegistry(t *testing.T) {
	c := NewDataSourceClient("http://x", nil)
	ohlcv := NewDataOHLCVTool(c)
	stocks := NewDataStocksTool(c)
	fund := NewDataFundamentalsTool(c)

	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(ohlcv))
	require.NoError(t, reg.Register(stocks))
	require.NoError(t, reg.Register(fund))

	list := reg.List()
	require.Len(t, list, 3)
	// Sorted: data.fundamentals < data.ohlcv < data.stocks
	assert.Equal(t, "data.fundamentals", list[0].Name)
	assert.Equal(t, "data.ohlcv", list[1].Name)
	assert.Equal(t, "data.stocks", list[2].Name)
}
