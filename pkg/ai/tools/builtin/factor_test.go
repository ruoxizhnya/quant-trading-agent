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

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/client"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
)

// ─── mock factor API server ───────────────────────────────────────────

// factorTestServer spins up an httptest.Server that mimics the factor
// service's /api/factor/compute and /api/factor/evaluate endpoints.
// It returns canned responses and records the last request body so
// tests can assert delegation.
type factorTestServer struct {
	server   *httptest.Server
	lastBody map[string]interface{}
	// If failWith is non-empty, the server returns HTTP 500 with that
	// message — simulates a downstream error.
	failWith string
	// If icOverride is non-nil, the evaluate endpoint returns that IC
	// (and a matching IR=IC*10) instead of the default 0.045. Used by
	// L2 gate tests to exercise both pass and fail paths.
	icOverride *float64
}

func newFactorTestServer() *factorTestServer {
	s := &factorTestServer{}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *factorTestServer) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.lastBody = body

	if s.failWith != "" {
		http.Error(w, s.failWith, 500)
		return
	}

	switch r.URL.Path {
	case "/api/factor/compute":
		// Return a canned ComputeFactorResponse.
		resp := client.ComputeFactorResponse{
			Values: map[string][]float64{
				"000001.SZ": {1.1, 1.2, 1.3},
				"600000.SH": {2.1, 2.2, 2.3},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)

	case "/api/factor/evaluate":
		ic := 0.045
		ir := 0.62
		if s.icOverride != nil {
			ic = *s.icOverride
			ir = ic * 10.0 // arbitrary but stable for tests
		}
		resp := client.ComputeFactorResponse{
			IC: ic,
			IR: ir,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)

	default:
		http.Error(w, "not found", 404)
	}
}

func (s *factorTestServer) close() { s.server.Close() }

// ─── FactorComputeTool tests ──────────────────────────────────────────

func TestFactorComputeTool_Name(t *testing.T) {
	tt := NewFactorComputeTool(client.NewFactorClient("http://x"))
	assert.Equal(t, "factor.compute", tt.Name())
}

func TestFactorComputeTool_Description(t *testing.T) {
	tt := NewFactorComputeTool(client.NewFactorClient("http://x"))
	assert.NotEmpty(t, tt.Description())
	assert.Contains(t, tt.Description(), "factor")
}

func TestFactorComputeTool_Parameters(t *testing.T) {
	tt := NewFactorComputeTool(client.NewFactorClient("http://x"))
	params := tt.Parameters()
	require.Len(t, params, 4)
	for _, p := range params {
		assert.True(t, p.Required)
	}
	assert.Equal(t, "formula", params[0].Name)
	assert.Equal(t, "symbols", params[1].Name)
}

func TestFactorComputeTool_OutputSchema(t *testing.T) {
	tt := NewFactorComputeTool(client.NewFactorClient("http://x"))
	schema := tt.OutputSchema()
	assert.Equal(t, "object", schema.Type)
}

func TestFactorComputeTool_Execute_HappyPath(t *testing.T) {
	srv := newFactorTestServer()
	defer srv.close()

	tt := NewFactorComputeTool(client.NewFactorClient(srv.server.URL))
	args := map[string]interface{}{
		"formula":    "ts_rank(close, 20)",
		"symbols":    []string{"000001.SZ", "600000.SH"},
		"start_date": "2022-01-01",
		"end_date":   "2024-01-01",
	}

	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	require.NotNil(t, result)

	values, ok := result.(map[string][]float64)
	require.True(t, ok, "result should be map[string][]float64, got %T", result)
	assert.Len(t, values, 2)
	assert.Equal(t, []float64{1.1, 1.2, 1.3}, values["000001.SZ"])

	// Verify the server received the right request body.
	assert.Equal(t, "ts_rank(close, 20)", srv.lastBody["formula"])
}

func TestFactorComputeTool_Execute_MissingFormula(t *testing.T) {
	srv := newFactorTestServer()
	defer srv.close()
	tt := NewFactorComputeTool(client.NewFactorClient(srv.server.URL))

	args := map[string]interface{}{
		"symbols":    []string{"000001.SZ"},
		"start_date": "2022-01-01",
		"end_date":   "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "formula")
}

func TestFactorComputeTool_Execute_MissingSymbols(t *testing.T) {
	srv := newFactorTestServer()
	defer srv.close()
	tt := NewFactorComputeTool(client.NewFactorClient(srv.server.URL))

	args := map[string]interface{}{
		"formula":    "close",
		"start_date": "2022-01-01",
		"end_date":   "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "symbols")
}

func TestFactorComputeTool_Execute_DownstreamError(t *testing.T) {
	srv := newFactorTestServer()
	srv.failWith = "factor service is down"
	defer srv.close()

	tt := NewFactorComputeTool(client.NewFactorClient(srv.server.URL))
	args := map[string]interface{}{
		"formula":    "close",
		"symbols":    []string{"000001.SZ"},
		"start_date": "2022-01-01",
		"end_date":   "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	// Should NOT be ErrInvalidArgs — args were valid.
	assert.False(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "factor.compute")
}

func TestNewFactorComputeTool_NilClientPanics(t *testing.T) {
	assert.Panics(t, func() { NewFactorComputeTool(nil) })
}

// ─── FactorEvaluateTool tests ─────────────────────────────────────────

func TestFactorEvaluateTool_Name(t *testing.T) {
	tt := NewFactorEvaluateTool(client.NewFactorClient("http://x"))
	assert.Equal(t, "factor.evaluate", tt.Name())
}

func TestFactorEvaluateTool_Description(t *testing.T) {
	tt := NewFactorEvaluateTool(client.NewFactorClient("http://x"))
	assert.NotEmpty(t, tt.Description())
	assert.Contains(t, tt.Description(), "IC")
}

func TestFactorEvaluateTool_Parameters(t *testing.T) {
	tt := NewFactorEvaluateTool(client.NewFactorClient("http://x"))
	params := tt.Parameters()
	require.Len(t, params, 4)
	for _, p := range params {
		assert.True(t, p.Required)
	}
}

func TestFactorEvaluateTool_OutputSchema(t *testing.T) {
	tt := NewFactorEvaluateTool(client.NewFactorClient("http://x"))
	schema := tt.OutputSchema()
	assert.Equal(t, "object", schema.Type)
	// Should list IC and IR fields.
	found := map[string]bool{}
	for _, f := range schema.Fields {
		found[f.Name] = true
	}
	assert.True(t, found["ic"])
	assert.True(t, found["ir"])
}

func TestFactorEvaluateTool_Execute_HappyPath(t *testing.T) {
	srv := newFactorTestServer()
	defer srv.close()

	tt := NewFactorEvaluateTool(client.NewFactorClient(srv.server.URL))
	args := map[string]interface{}{
		"formula":    "ts_rank(close, 20)",
		"symbols":    []string{"000001.SZ", "600000.SH"},
		"start_date": "2022-01-01",
		"end_date":   "2024-01-01",
	}

	result, err := tt.Execute(context.Background(), args)
	require.NoError(t, err)
	require.NotNil(t, result)

	metrics, ok := result.(*client.FactorMetrics)
	require.True(t, ok, "result should be *client.FactorMetrics, got %T", result)
	assert.InDelta(t, 0.045, metrics.IC, 1e-9)
	assert.InDelta(t, 0.62, metrics.IR, 1e-9)
}

func TestFactorEvaluateTool_Execute_MissingFormula(t *testing.T) {
	srv := newFactorTestServer()
	defer srv.close()
	tt := NewFactorEvaluateTool(client.NewFactorClient(srv.server.URL))

	args := map[string]interface{}{
		"symbols":    []string{"000001.SZ"},
		"start_date": "2022-01-01",
		"end_date":   "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.True(t, errors.Is(err, tools.ErrInvalidArgs))
}

func TestFactorEvaluateTool_Execute_DownstreamError(t *testing.T) {
	srv := newFactorTestServer()
	srv.failWith = "evaluation failed"
	defer srv.close()

	tt := NewFactorEvaluateTool(client.NewFactorClient(srv.server.URL))
	args := map[string]interface{}{
		"formula":    "close",
		"symbols":    []string{"000001.SZ"},
		"start_date": "2022-01-01",
		"end_date":   "2024-01-01",
	}
	_, err := tt.Execute(context.Background(), args)
	require.Error(t, err)
	assert.False(t, errors.Is(err, tools.ErrInvalidArgs))
	assert.Contains(t, err.Error(), "factor.evaluate")
}

func TestNewFactorEvaluateTool_NilClientPanics(t *testing.T) {
	assert.Panics(t, func() { NewFactorEvaluateTool(nil) })
}

// ─── Registry integration ─────────────────────────────────────────────

func TestFactorTools_RegisterInRegistry(t *testing.T) {
	c := client.NewFactorClient("http://x")
	compute := NewFactorComputeTool(c)
	evaluate := NewFactorEvaluateTool(c)

	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(compute))
	require.NoError(t, reg.Register(evaluate))

	list := reg.List()
	require.Len(t, list, 2)
	// Sorted by name: factor.compute < factor.evaluate
	assert.Equal(t, "factor.compute", list[0].Name)
	assert.Equal(t, "factor.evaluate", list[1].Name)
}
