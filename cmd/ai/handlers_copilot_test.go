package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/copilot"
)

// newCopilotTestRouter 只装 copilot 路由组，不需要 DB / config / 重装配。
// 范式同 handlers_openapi_test.go 的 newOpenAPITestRouter。
func newCopilotTestRouter() *gin.Engine {
	router := gin.New()
	registerCopilotRoutes(router, copilot.NewCopilotService(), nil)
	return router
}

// TestCopilotSaveRouteIsNotRegistered 是 AUD-01 (ODR-065 C3) 的回归护栏。
//
// 断言的是「路由不存在」，不是「坏输入被拒绝」。后一种写法在端点被重新
// 加回来时**依然会通过** —— 因为加回来的实现也可能对遍历名返回 400，
// 那就成了假护栏。本仓吃过一次亏（P1-8 的教训）：新加的校验必须先故意
// 破坏一次、确认它真能报错，再说它有用。这里同理 —— 把
// `copilot.POST("/save", ...)` 加回 buildRouter，这条测试必须变红。
func TestCopilotSaveRouteIsNotRegistered(t *testing.T) {
	router := newCopilotTestRouter()

	body := `{"code":"package plugins\nimport \"os\"\nfunc f(){ os.RemoveAll(\"/\") }","strategy_name":"X"}`
	req := httptest.NewRequest(http.MethodPost, "/api/copilot/save", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code,
		"POST /api/copilot/save 必须不存在（AUD-01 已按「删除」裁决移除该端点）。"+
			"若要重新加回：先重新评估路径遍历风险，并配套 StrategyName 白名单 + "+
			"staticcheck 闸 + filepath.Join，不能只恢复原实现")
}

// TestCopilotOtherRoutesStillRegistered 是对上一条的反面对照。
//
// 没有它，上一条在 registerCopilotRoutes 被整体删掉 / 改名 / 注册到别的
// 路径前缀时会因为「全都没注册」而误判为通过 —— 又是一个假护栏。
// 所以必须同时钉住「该在的还在」。
func TestCopilotOtherRoutesStillRegistered(t *testing.T) {
	router := newCopilotTestRouter()

	registered := make(map[string]bool)
	for _, r := range router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}

	assert.True(t, registered["POST /api/copilot/generate"],
		"generate 路由应仍注册（AUD-01 只删 save）")
	assert.True(t, registered["GET /api/copilot/generate/:job_id"],
		"generate 任务查询路由应仍注册")
	assert.True(t, registered["GET /api/copilot/stats"],
		"stats 路由应仍注册")
	assert.False(t, registered["POST /api/copilot/save"],
		"save 路由必须不存在")
}
