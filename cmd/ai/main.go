package main

// ai-service（cmd/ai）—— AI 实验员侧的进程边界（ADR-027 §5 第 8 步的
// 前置切片，AI 拆仓阶段 1）。
//
// 职责：承载 Copilot / pipeline / explore / tools 四族 handler 与 Tools
// Registry 的装配，让「试什么」（AI 领域）住进自己的进程；analysis-service
// 对这四族路由做纯反代（cmd/analysis/handlers_ai_proxy.go），前端与
// openapi 契约零改动。
//
// 依赖方向（拆仓后的不变量，repoguard/ai_boundary 盯）：本进程 import
// core 的 pkg/*（引擎/存储/风控/表达式）—— 单向；core 侧对 pkg/ai 的
// 引用在阶段 1 仍存在（BuildCopilot / BuildToolsRegistry），
// 阶段 2 本文件与 pkg/ai 一起搬进 quant-trading-agent 仓，core 归零。
//
// 回测行为与 analysis-service 的对等性：engine 装配（RiskManager /
// MockTrader / Store / WF 工厂）与 analysis 的 main 逐项一致 ——
// 同一个实验在两个进程跑出不同结果是不可接受的（可复现性）。

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/quant-trading/pkg/bootstrap"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/pipeline"
	"github.com/ruoxizhnya/quant-trading/pkg/backtest"
	"github.com/ruoxizhnya/quant-trading/pkg/marketdata"
	"github.com/ruoxizhnya/quant-trading/pkg/observability"
	"github.com/spf13/viper"
)

func main() {
	logger := bootstrap.InitLogger()
	m := bootstrap.InitMetrics(logger)
	v := bootstrap.LoadConfig(logger, "config/ai-service.yaml")

	// ── 引擎装配（与 analysis-service main 逐项对等）────────────────
	engine, httpProvider := bootstrap.BuildBacktestEngine(v, logger)
	riskManager := bootstrap.BuildRiskManager(v, logger)
	engine.SetRiskManager(riskManager)
	executionTrader := bootstrap.BuildExecutionTrader(v, logger)
	engine.SetLiveTrader(executionTrader)

	store := bootstrap.InitStore(v, logger)
	engine.SetStore(store)

	authSvc := bootstrap.InitAuth(v, store, logger)

	// walk-forward 的每个窗口必须拿到**独立**的 Engine 实例（引擎级缓存
	// 是 per-instance 的，共享单例会让并发窗口互相污染 —— 与 analysis
	// main 同一条注释、同一个坑）。
	newEngine := func() (*backtest.Engine, error) {
		eng, err := backtest.NewEngine(v, httpProvider, logger)
		if err != nil {
			return nil, err
		}
		eng.SetRiskManager(riskManager)
		eng.SetLiveTrader(executionTrader)
		eng.SetStore(store)
		return eng, nil
	}

	pgProvider := marketdata.NewPostgresProvider(store, logger)
	dataAdapter := marketdata.NewDataAdapter(nil, pgProvider, httpProvider, logger)
	wfEngine := backtest.NewWalkForwardEngine(func() (*backtest.Engine, error) {
		eng, err := newEngine()
		if err != nil {
			return nil, err
		}
		// 新引擎必须挂上 dataAdapter，否则多源回退路径不生效（与
		// analysis 的 buildDataServices 同一条注释）。
		eng.SetDataAdapter(dataAdapter)
		eng.SetStore(store)
		return eng, nil
	}, store, logger)
	logger.Info().Msg("Walk-forward engine initialized (tools-side)")

	copilotService, copilotRunner := BuildCopilot(v, engine, logger)

	factorPool := gene_pool.NewFactorPool(store.DB())
	strategyPool := gene_pool.NewStrategyPool(store.DB())
	wfRunner := NewWalkForwardEngineAdapter(wfEngine)

	// factor.compute / factor.evaluate / compute_factor_ic 经 HTTP 打
	// analysis-service 的 /api/factor/*（只在那里注册）。
	toolsRegistry := BuildToolsRegistry(
		v,
		analysisFactorAPIURL(v),
		copilotRunner,
		wfRunner,
		factorPool,
		strategyPool,
		riskManager,
		store,
		store,
		logger,
	)

	bootstrap.ApplyGinMode(v, logger)
	router := bootstrap.BuildRouter(authSvc, v, logger)

	// 健康探针（local-stack netstat 断言 + compose healthcheck 消费）。
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "ai-service",
			"status":  "ok",
		})
	})
	router.GET("/metrics", observability.Handler(m))

	// ── 四族 AI 路由（自 analysis 迁入，路径不变）────────────────────
	registerCopilotRoutes(router, copilotService, copilotRunner)

	// P1-1b：实验日志落点。显式判空而非直接传 store —— *PostgresStore 的
	// nil 塞进接口会变成非 nil 接口值（P0-5 的 aiClient 同款坑）。
	var expSink pipeline.ExperimentSink
	if store != nil {
		expSink = store
	}
	registerPipelineRoutes(router, copilotRunner, expSink)
	registerExploreRoutes(router, copilotRunner, expSink, engine)

	// AUD-02 (ODR-065 H5)：WithToolsAuth 按工具的审计副作用类做 per-tool
	// RBAC —— save_factor / save_strategy 要 trader-or-admin，其余未分类
	// 一律 admin（fail-closed）。
	NewToolsHandler(toolsRegistry, logger, WithToolsAuth(authSvc)).RegisterRoutes(router)

	srv := bootstrap.StartHTTPServer(router, v, logger, "AI Service (ai-service)")

	sig := bootstrap.WaitForShutdown()
	logger.Info().Str("signal", sig.String()).Msg("Shutdown signal received; beginning graceful drain")
	bootstrap.GracefulShutdown(srv, nil, nil, store, logger)
}

// analysisFactorAPIURL 解析 analysis 侧 factor 端点基址
// （ai_service.factor_api_url，缺省 :8085）。
func analysisFactorAPIURL(v *viper.Viper) string {
	if u := v.GetString("ai_service.factor_api_url"); u != "" {
		return u
	}
	return "http://localhost:8085"
}
