package main

// AI 侧装配 builder —— 自 core pkg/bootstrap 随阶段 2 迁入（逐字搬运 +
// copilot.CopilotService → copilot.CopilotService 改名）。这些 builder
// import pkg/ai，必须住在 agent 仓（core 侧对 AI 层零依赖的不变量由
// core 的 repoguard ai_boundary 钉住）。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/copilot"
	"github.com/ruoxizhnya/quant-trading/pkg/bootstrap"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/client"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/tools/builtin"
	"github.com/ruoxizhnya/quant-trading/pkg/backtest"
	"github.com/ruoxizhnya/quant-trading/pkg/backtest/contracts"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/sandbox/runner"
	"github.com/ruoxizhnya/quant-trading/pkg/sandbox/staticcheck"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
	"github.com/spf13/viper"
)

// ── 来自 core pkg/bootstrap/copilot.go（AI 拆仓阶段 2 随迁，逐字搬运）──

// StrategyEngineAdapter 把 *backtest.Engine 适配成 copilot.BacktestRunner
// （窄接口）。copilot / pipeline / explore 三族 handler 与 tools 门面的
// backtest.run 都消费它 —— 零重复。
type StrategyEngineAdapter struct {
	engine *backtest.Engine
}

// NewStrategyEngineAdapter 构造引擎适配器。
func NewStrategyEngineAdapter(engine *backtest.Engine) *StrategyEngineAdapter {
	return &StrategyEngineAdapter{engine: engine}
}

// RunBacktest 实现 copilot.BacktestRunner。
func (a *StrategyEngineAdapter) RunBacktest(
	ctx context.Context,
	strategyName string,
	stockPool []string,
	startDate, endDate string,
) (*domain.BacktestResult, error) {
	req := backtest.BacktestRequest{
		Strategy:  strategyName,
		StockPool: stockPool,
		StartDate: startDate,
		EndDate:   endDate,
	}
	resp, err := a.engine.RunBacktest(ctx, req)
	if err != nil {
		return nil, err
	}
	return &domain.BacktestResult{
		TotalReturn:    resp.TotalReturn,
		AnnualReturn:   resp.AnnualReturn,
		SharpeRatio:    resp.SharpeRatio,
		SortinoRatio:   resp.SortinoRatio,
		MaxDrawdown:    resp.MaxDrawdown,
		WinRate:        resp.WinRate,
		TotalTrades:    resp.TotalTrades,
		WinTrades:      resp.WinTrades,
		LoseTrades:     resp.LoseTrades,
		AvgHoldingDays: resp.AvgHoldingDays,
		CalmarRatio:    resp.CalmarRatio,
	}, nil
}

// StaticCheckAdapter 实现 strategy.CodeChecker，委托给
// internal/sandbox/staticcheck。S7-P1-2（ODR-043）：定义在装配根，
// 使 pkg/strategy 不 import internal/sandbox —— 反向依赖在这里断。
type StaticCheckAdapter struct{}

// CheckOrError 实现 strategy.CodeChecker。
func (StaticCheckAdapter) CheckOrError(code string) error {
	return staticcheck.CheckOrError(code)
}

// SandboxRunnerAdapter 实现 strategy.BuildExecutor，委托给
// internal/sandbox/runner。Runner 以 30s 超时 + 1GiB 内存上限构造一次并
// 复用（Sprint 6 P1-11 / ODR-020）；Runner 除配置外无状态，复用安全。
type SandboxRunnerAdapter struct {
	r *runner.Runner
}

// envAllowUnenforcedSandboxLimits 是在无法强制资源限额的平台（当前是
// Windows，没有 setrlimit(2)）上运行 copilot build 沙箱的逃生舱。
//
// 没有它 runner 会 FAIL CLOSED：要了下面限额的构建直接被拒，而不是无界
// 运行。要点在于 —— 一个静默无上限的子进程比没有子进程更糟，因为下游
// 没人知道保护缺了。
//
// 部署环境绝不许设它。POSIX 上没有必要：限额在子进程内部强制。
const envAllowUnenforcedSandboxLimits = "SANDBOX_ALLOW_UNENFORCED_LIMITS"

// NewSandboxRunnerAdapter 构造沙箱 runner 适配器。
func NewSandboxRunnerAdapter(logger zerolog.Logger) *SandboxRunnerAdapter {
	opts := []runner.Option{
		runner.WithTimeout(30 * time.Second),
		runner.WithLimits(runner.Limits{
			MemoryBytes: 1 << 30, // 1 GiB
			CPUSeconds:  25,
			OpenFiles:   256,
		}),
	}

	if os.Getenv(envAllowUnenforcedSandboxLimits) != "" {
		logger.Warn().
			Str("env", envAllowUnenforcedSandboxLimits).
			Msg("sandbox resource limits will NOT be enforced on this platform; " +
				"unset this variable outside local development")
		opts = append(opts,
			runner.WithAllowUnenforcedLimits(),
			runner.WithOnUnenforcedLimits(func(argv []string, unenforced runner.Limits) {
				logger.Warn().
					Strs("argv", argv).
					Str("unenforced", unenforced.Describe()).
					Msg("sandbox build ran with some resource limits NOT enforced")
			}),
		)
	}

	return &SandboxRunnerAdapter{r: runner.New(opts...)}
}

// Run 实现 strategy.BuildExecutor。
func (a *SandboxRunnerAdapter) Run(ctx context.Context, name string, args []string, workingDir string) (*bytes.Buffer, *bytes.Buffer, error) {
	return a.r.Run(ctx, name, args, runner.Options{Dir: workingDir})
}

// IsTimeout 实现 strategy.BuildExecutor。
func (a *SandboxRunnerAdapter) IsTimeout(err error) bool {
	return errors.Is(err, runner.ErrTimeout)
}

// BuildCopilot 构造 Copilot service（S7-P1-2, ODR-043）：LLM client、
// code checker、build executor 全部在装配根注入。返回 service 与委托给
// 回测引擎的 BacktestRunner 适配器。
func BuildCopilot(v *viper.Viper, engine *backtest.Engine, logger zerolog.Logger) (*copilot.CopilotService, copilot.BacktestRunner) {
	copilotService := copilot.NewCopilotService().
		WithLLMClient(ai.NewClient()).
		WithCodeChecker(StaticCheckAdapter{}).
		WithBuildExecutor(NewSandboxRunnerAdapter(logger)).
		WithLogger(logger.With().Str("component", "copilot").Logger()).
		WithWorkingDir(v.GetString("copilot.working_dir"))
	logger.Info().
		Bool("ai_configured", copilotService.IsConfigured()).
		Str("working_dir", copilotService.WorkingDir()).
		Msg("Copilot service initialized")
	copilotRunner := NewStrategyEngineAdapter(engine)
	return copilotService, copilotRunner
}

// WalkForwardEngineAdapter 把 *backtest.WalkForwardEngine 适配成
// builtin.WalkForwardRunner（窄接口）。具体引擎的 RunWalkForward 收
// Request struct，工具接口收散参 —— 桥接在装配根完成，工具层与引擎
// DTO 解耦（S7-P3-4，Hermes Phase 1.7）。
type WalkForwardEngineAdapter struct {
	engine *backtest.WalkForwardEngine
}

// NewWalkForwardEngineAdapter 构造 walk-forward 适配器。
func NewWalkForwardEngineAdapter(engine *backtest.WalkForwardEngine) *WalkForwardEngineAdapter {
	return &WalkForwardEngineAdapter{engine: engine}
}

// RunWalkForward 实现 builtin.WalkForwardRunner。
func (a *WalkForwardEngineAdapter) RunWalkForward(
	ctx context.Context,
	strategyName string,
	stockPool []string,
	startDate, endDate string,
	params domain.WalkForwardParams,
) (*domain.WalkForwardReport, error) {
	req := backtest.WalkForwardRequest{
		Strategy:          strategyName,
		StockPool:         stockPool,
		StartDate:         startDate,
		EndDate:           endDate,
		WalkForwardParams: params,
	}
	return a.engine.RunWalkForward(ctx, req)
}

// ── 来自 core pkg/bootstrap/tools.go（AI 拆仓阶段 2 随迁，逐字搬运）──

// BuildToolsRegistry 构造 Tools Registry（S7-P3-3, ODR-043）并注册全部
// 内置工具组。registry 经 ToolsHandler 暴露在 /api/tools/*，让外部 agent
// 服务（如 Hermes）无需读 SPEC.md 即可发现与调用平台能力。
//
// S7-P3-4（Hermes Phase 1.7 + Phase 2.2-2.3）起 hosting 19 个工具、12 组：
//   - backtest.run          (S7-P3-3, L3 gate)
//   - factor.compute/.evaluate / validate_factor / compute_factor_ic
//   - data.ohlcv/.stocks/.fundamentals
//   - strategy.list/.get
//   - walk_forward_validate (Hermes Phase 1.3, L4 gate)
//   - list_factors / save_factor / list_strategies / save_strategy（gene pool）
//   - summarize_backtest / get_strategy_lineage / get_market_regime
//   - research.profile (EQD-P2-1) / factor.hypothesis (P2-3) / strategy_health (P2-6)
//
// Wiring notes:
//   - BacktestTool 复用与 AI pipeline 相同的 contracts.BacktestRunner（零重复）。
//   - FactorTool 走 HTTP 指向 factorAPIURL —— 拆出 ai-service 后该 URL 指向
//     analysis-service（/api/factor/* 只在那里注册），不再「指向自身」。
//   - DataFetchTool 用共享 HTTPClient 指向 data-service URL。
//   - StrategyRegistryTool 读包级 strategy.DefaultRegistry，无需装配。
//   - Gene Pool tools 收窄接口，由 *gene_pool.FactorPool / StrategyPool 满足。
//   - ResearchProfileTool 收 *storage.PostgresStore + equitydeep.vault_path。
func BuildToolsRegistry(
	v *viper.Viper,
	factorAPIURL string,
	runner contracts.BacktestRunner,
	wfRunner builtin.WalkForwardRunner,
	factorPool builtin.FactorPoolClient,
	strategyPool builtin.StrategyPoolClient,
	regimeDetector builtin.RegimeDetectorClient,
	researchProfile builtin.ResearchProfileClient,
	hypothesisStore *storage.PostgresStore,
	logger zerolog.Logger,
) *tools.Registry {
	reg := tools.NewRegistry()

	// ── Group 1: Backtest (S7-P3-3) ──────────────────────────────────
	if err := reg.Register(builtin.NewBacktestTool(runner)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register backtest.run tool")
	}

	// ── Group 2: Factor — HTTP + expression-based (S7-P3-3 + Hermes 1.1/1.2) ─
	factorClient := client.NewFactorClient(factorAPIURL)
	if err := reg.Register(builtin.NewFactorComputeTool(factorClient)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register factor.compute tool")
	}
	if err := reg.Register(builtin.NewFactorEvaluateTool(factorClient)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register factor.evaluate tool")
	}
	// Hermes Phase 1.1: L1 syntax gate — no DI, creates fresh parser per Execute.
	if err := reg.Register(builtin.NewValidateFactorTool()); err != nil {
		logger.Fatal().Err(err).Msg("failed to register validate_factor tool")
	}
	// Hermes Phase 1.2: L2 quick IC gate — reuses the same factor HTTP client.
	if err := reg.Register(builtin.NewComputeFactorICTool(factorClient)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register compute_factor_ic tool")
	}

	// ── Group 3: Data fetch (S7-P3-3) ───────────────────────────────
	dataServiceURL := v.GetString("data_service.url")
	if dataServiceURL == "" {
		dataServiceURL = "http://localhost:8081"
	}
	dataClient := builtin.NewDataSourceClient(dataServiceURL, bootstrap.HTTPClient)
	if err := reg.Register(builtin.NewDataOHLCVTool(dataClient)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register data.ohlcv tool")
	}
	if err := reg.Register(builtin.NewDataStocksTool(dataClient)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register data.stocks tool")
	}
	if err := reg.Register(builtin.NewDataFundamentalsTool(dataClient)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register data.fundamentals tool")
	}

	// ── Group 4: Strategy registry (S7-P3-3) ─────────────────────────
	if err := reg.Register(builtin.NewStrategyListTool()); err != nil {
		logger.Fatal().Err(err).Msg("failed to register strategy.list tool")
	}
	if err := reg.Register(builtin.NewStrategyGetTool()); err != nil {
		logger.Fatal().Err(err).Msg("failed to register strategy.get tool")
	}

	// ── Group 5: Walk-forward validation (Hermes Phase 1.3, L4 gate) ─
	if err := reg.Register(builtin.NewWalkForwardValidateTool(wfRunner)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register walk_forward_validate tool")
	}

	// ── Group 6: Gene Pool — factor + strategy CRUD (Hermes Phase 1.4/1.5) ─
	if err := reg.Register(builtin.NewListFactorsTool(factorPool)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register list_factors tool")
	}
	if err := reg.Register(builtin.NewSaveFactorTool(factorPool)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register save_factor tool")
	}
	if err := reg.Register(builtin.NewListStrategiesTool(strategyPool)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register list_strategies tool")
	}
	if err := reg.Register(builtin.NewSaveStrategyTool(strategyPool)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register save_strategy tool")
	}

	// ── Group 7: Summarization (Hermes Phase 1.6) ───────────────────
	if err := reg.Register(builtin.NewSummarizeBacktestTool()); err != nil {
		logger.Fatal().Err(err).Msg("failed to register summarize_backtest tool")
	}

	// ── Group 8: Lineage (Hermes Phase 2.2, gene pool lineage) ─────
	if err := reg.Register(builtin.NewGetStrategyLineageTool(strategyPool)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register get_strategy_lineage tool")
	}

	// ── Group 9: Market regime (Hermes Phase 2.3) ──────────────────
	// Reuses the dataClient from Group 3 for OHLCV fetching.
	if err := reg.Register(builtin.NewGetMarketRegimeTool(regimeDetector, dataClient)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register get_market_regime tool")
	}

	// ── Group 10: Research profile (EQD-P2-1, bridge B2) ───────────
	// Reads the EquityDeep research archive, preferring the research.*
	// projection and falling back to the contract C2 mirror under
	// equitydeep.vault_path (env EQUITYDEEP_VAULT_PATH). The vault is
	// mounted read-only; an empty path simply disables the fallback.
	if err := reg.Register(builtin.NewResearchProfileTool(researchProfile, v.GetString("equitydeep.vault_path"))); err != nil {
		logger.Fatal().Err(err).Msg("failed to register research.profile tool")
	}

	// ── Group 11: Factor hypothesis (P2-3) ─────────────────────────
	// 「这个因子凭什么有效」—— 采用一个因子之前先看它的机制来自哪里。
	//
	// 显式判空而不是直接传 hypothesisStore：*PostgresStore 的 nil 塞进
	// 接口会变成非 nil 的接口值，工具会拿着空指针去查库（P1-1b 的老坑）。
	// 没连库时工具仍可用 —— 它回退到 domain 里的内置假设表。
	var hs builtin.FactorHypothesisStore
	if hypothesisStore != nil {
		hs = hypothesisStore
	}
	if err := reg.Register(builtin.NewFactorHypothesisTool(hs)); err != nil {
		logger.Fatal().Err(err).Msg("failed to register factor.hypothesis tool")
	}

	// ── Group 12: Strategy health (P2-6) ───────────────────────────
	// 「这个策略是不是开始不行了」—— 滚动指标 + 概念漂移检测。
	// 接上之前 pkg/ai/drift 与 pkg/strategy/monitor 是两个零调用方的孤儿包，
	// 实现完整却没人消费。无状态：每次调用新建 monitor 喂完整段序列。
	if err := reg.Register(builtin.NewStrategyHealthTool()); err != nil {
		logger.Fatal().Err(err).Msg("failed to register monitor.strategy_health tool")
	}

	logger.Info().
		Int("tool_count", len(reg.List())).
		Msg("Tools Registry initialized (S7-P3-3 + Hermes Phase 1.7 + Phase 2.2-2.3 + EQD-P2-1): 19 tools exposed at /api/tools/* — backtest/factor/data/strategy/gene-pool/walk-forward/summarize/lineage/regime/research")
	return reg
}

// FactorAPIURL 从 server.port 推导 analysis 侧 factor 端点基址
// （analysis 自指；ai-service 则显式传 analysis 的地址）。
func FactorAPIURL(v *viper.Viper, defaultPort int) string {
	if port := v.GetInt("server.port"); port != 0 {
		return fmt.Sprintf("http://localhost:%d", port)
	}
	return fmt.Sprintf("http://localhost:%d", defaultPort)
}
