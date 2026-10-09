package main

// AUD-02 (ODR-065 H5) regression guard —— tools 侧（随 ai-service 迁入）。
// 原 cmd/analysis/handlers_rbac_test.go 的 tools 段；execution 段留在
// analysis（那里才有 NewExecutionHandler）。语义逐字保留。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading/pkg/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── tools: per-tool RBAC ─────────────────────────────────────────────

// tokenFor 与 analysis 侧 handlers_rbac_test.go 同名 helper 语义一致
// （两包各自独立，允许同形重复 —— 测试 helper 不构成生产双真相）。
func tokenFor(t *testing.T, svc *auth.Service, role auth.Role) string {
	t.Helper()
	tok, _, err := svc.IssueTokens(&auth.User{ID: 1, Username: string(role), Role: role})
	require.NoError(t, err)
	return tok
}

func toolsRBACRouter(t *testing.T, svc *auth.Service) *gin.Engine {
	t.Helper()
	r := gin.New()
	r.Use(svc.Middleware())

	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(newStubTool("save_factor")))
	require.NoError(t, reg.Register(newStubTool("backtest.run")))

	NewToolsHandler(reg, zerolog.Nop(), WithToolsAuth(svc)).RegisterRoutes(r)
	return r
}

// stubTool is a minimal Tool whose registry name is set explicitly, so
// the test can place it in the side-effect table's read or write class
// via a name that is already classified.
//
// It deliberately uses REAL classified names (save_factor /
// backtest.run) so the test exercises the production classification
// table rather than a test-only map.
type stubTool struct {
	name string
}

func newStubTool(name string) *stubTool { return &stubTool{name: name} }

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub for RBAC tests" }
func (s *stubTool) Parameters() []tools.Parameter {
	return []tools.Parameter{{Name: "x", Type: "string"}}
}
func (s *stubTool) OutputSchema() tools.OutputSchema {
	return tools.OutputSchema{Type: "object"}
}
func (s *stubTool) Execute(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	return map[string]string{"stub": s.name}, nil
}

func callTool(t *testing.T, r *gin.Engine, name, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tools/"+name,
		strings.NewReader(`{"args":{}}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestTools_WriteTool_ViewerForbidden(t *testing.T) {
	t.Parallel()
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("test")})
	r := toolsRBACRouter(t, svc)

	w := callTool(t, r, "save_factor", tokenFor(t, svc, auth.RoleViewer))
	assert.Equal(t, http.StatusForbidden, w.Code,
		"viewer must not be able to run a write-side-effect tool; body=%s", w.Body.String())

	// The error body should name the class so an operator can tell
	// "you lack the role" from "the tool failed".
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "INSUFFICIENT_ROLE", body["code"])
	assert.Equal(t, "write", body["effect"])
}

func TestTools_WriteTool_TraderAllowed(t *testing.T) {
	t.Parallel()
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("test")})
	r := toolsRBACRouter(t, svc)

	w := callTool(t, r, "save_factor", tokenFor(t, svc, auth.RoleTrader))
	// The stub doesn't implement the real Tool interface contract for
	// execution; what matters is that RBAC did NOT block it (i.e. the
	// status is not 403).
	assert.NotEqual(t, http.StatusForbidden, w.Code,
		"trader must be allowed past RBAC on a write tool")
}

func TestTools_ReadTool_ViewerAllowed(t *testing.T) {
	t.Parallel()
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("test")})
	r := toolsRBACRouter(t, svc)

	w := callTool(t, r, "backtest.run", tokenFor(t, svc, auth.RoleViewer))
	assert.NotEqual(t, http.StatusForbidden, w.Code,
		"viewer must be allowed past RBAC on a read tool")
}

// TestTools_UnclassifiedTool_FailsClosed is the fail-closed guard: a
// tool with no row in the classification table must require admin.
func TestTools_UnclassifiedTool_FailsClosed(t *testing.T) {
	t.Parallel()
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("test")})

	r := gin.New()
	r.Use(svc.Middleware())
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(newStubTool("brand.new.unclassified")))
	NewToolsHandler(reg, zerolog.Nop(), WithToolsAuth(svc)).RegisterRoutes(r)

	// trader is NOT enough for an unclassified tool.
	w := callTool(t, r, "brand.new.unclassified", tokenFor(t, svc, auth.RoleTrader))
	assert.Equal(t, http.StatusForbidden, w.Code,
		"unclassified tool must fail closed to admin-only")

	// admin gets through.
	w = callTool(t, r, "brand.new.unclassified", tokenFor(t, svc, auth.RoleAdmin))
	assert.NotEqual(t, http.StatusForbidden, w.Code)
}

func TestTools_AuthDisabled_OpenAccess(t *testing.T) {
	t.Parallel()
	svc := auth.NewService(nil, auth.Config{})
	require.False(t, svc.Enabled())

	r := gin.New()
	r.Use(svc.Middleware())
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(newStubTool("save_factor")))
	NewToolsHandler(reg, zerolog.Nop(), WithToolsAuth(svc)).RegisterRoutes(r)

	w := callTool(t, r, "save_factor", "")
	assert.NotEqual(t, http.StatusForbidden, w.Code,
		"auth disabled must stay open-access for tools too")
}

// TestTools_ListEndpoint_NotGated ensures the discovery endpoints
// (GET /api/tools) were not caught by the per-tool RBAC.
func TestTools_ListEndpoint_NotGated(t *testing.T) {
	t.Parallel()
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("test")})
	r := toolsRBACRouter(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	req.Header.Set("Authorization", "Bearer "+tokenFor(t, svc, auth.RoleViewer))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "viewer must be able to list tools")
}
