package main

import (
	"context"
	"github.com/ruoxizhnya/quant-trading/pkg/httpserver"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/copilot"
)

// ── AUD-01 (ODR-065 C3): POST /api/copilot/save 已删除，不是加固 ─────────
//
// 原实现把 req.Code 原样写到 ./pkg/strategy/plugins/strategy_<name>.go，
// StrategyName 无任何校验 → 路径遍历可写任意 .go 文件。
//
// 选删除而不是加固的三个理由（都有实测依据，不是口味问题）：
//
//  1. **零生产调用方**。前端 web/src/api/copilot.ts 在 S7-P2-7 就把
//     saveStrategy stub 删了，注释写明「None of these had any live
//     consumers」；全仓只剩 openapi 文档和一个 e2e 用例提到它。
//  2. **用途与 ADR-024 冲突**。ADR-024 定的是「LLM 生成的 Go 只是可审阅
//     artifact，不加载、不执行」，而本端点的全部作用就是把 Go 源码写进
//     磁盘。generate 流水线已经提供了正确的路径（staticcheck 闸 → 沙箱
//     内 `go build` → 临时目录用完即删）。本端点是旧架构的遗留物。
//  3. **删除后攻击面归零**，严格优于「加固后仍然存在」。
//
// ⚠️ 报告把危害描述为「写入即加载 → 等同远程代码执行」，**这一步不准确**：
// pkg/strategy/loader.go 的 PluginLoader.Watch 只挑 `.so`（见
// checkAndReload 的 `strings.HasSuffix(entry.Name(), ".so")`），而本端点写的是
// `.go` **源码**，不会被加载执行。真实危害是「任意 .go 文件写入」——
// 覆盖仓库内任意源文件，在下一次构建时被编译进二进制（构建投毒），
// 或写进 plugins 目录让构建失败。仍然严重，但链路比报告说的长一步。
//
// 回归护栏见 handlers_copilot_test.go：断言该路由**不存在**（404），
// 而不是断言它拒绝坏输入 —— 后者会在端点被重新加回来时依然通过。

// registerCopilotRoutes 注册 copilot 路由组（generate / generate/:job_id / stats）。
// AUD-01 之后本组**不再包含** POST /save，原因见上方说明块。
func registerCopilotRoutes(router *gin.Engine, copilotService *copilot.CopilotService, copilotRunner copilot.BacktestRunner) {
	copilotGroup := router.Group("/api/copilot")
	{
		copilotGroup.POST("/generate", func(c *gin.Context) {
			if !copilotService.IsConfigured() {
				httpserver.Fail(c, http.StatusServiceUnavailable, "AI not configured (set AI_API_KEY and AI_API_URL)")
				return
			}
			var req copilot.GenerateParams
			if err := c.ShouldBindJSON(&req); err != nil {
				httpserver.Wrap(c, http.StatusBadRequest, err, "invalid request: ")
				return
			}
			result := copilotService.Generate(c.Request.Context(), req, copilotRunner)
			// S7-P0-12 (ODR-043): Lock before reading Status. Generate()
			// spawns a goroutine that writes result.Status under
			// result.Lock(); reading it without synchronizing is a data
			// race. JobID is immutable after creation so it's safe to
			// read unlocked, but Status must be read under the lock.
			result.Lock()
			status := result.Status
			result.Unlock()
			c.JSON(http.StatusAccepted, gin.H{
				"job_id": result.JobID,
				"status": status,
			})
		})

		copilotGroup.GET("/generate/:job_id", func(c *gin.Context) {
			jobID := c.Param("job_id")
			result := copilotService.GetJob(jobID)
			if result == nil {
				httpserver.Fail(c, http.StatusNotFound, "job not found")
				return
			}
			result.Lock()
			status := result.Status
			code := result.Code
			buildErr := result.BuildErr
			btResult := result.BacktestResult
			btErr := result.BacktestErr
			strategyName := result.StrategyName
			result.Unlock()

			resp := gin.H{
				"job_id": jobID,
				"status": status,
			}
			if code != "" {
				resp["generated_code"] = code
			}
			if buildErr != "" {
				resp["build_error"] = buildErr
			}
			if strategyName != "" {
				resp["strategy_name"] = strategyName
			}
			if btErr != "" {
				resp["backtest_error"] = btErr
			}
			if btResult != nil {
				resp["backtest_result"] = btResult
			}
			c.JSON(http.StatusOK, resp)
		})

		copilotGroup.GET("/stats", func(c *gin.Context) {
			generated, buildable, backtested := copilotService.Stats()
			rate := copilotService.AcceptanceRate()
			c.JSON(http.StatusOK, gin.H{
				"generated":       generated,
				"buildable":       buildable,
				"backtest_valid":  backtested,
				"acceptance_rate": rate,
			})
		})

		// AUD-01 (ODR-065 C3): POST /save 已删除（原 saveStrategyHandler）。
		// 见文件内 saveStrategyHandler 位置的注释块。
	}

	_ = context.Background()
}
