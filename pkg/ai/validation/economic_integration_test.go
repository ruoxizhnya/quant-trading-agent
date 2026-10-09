package validation

// P2-9b 的端到端取证：真回测引擎（离线）→ 真 BacktestResult → 经济校验器。
//
// 为什么必须有这个文件：校验器本身跑得再对，如果它的输入（换手率）在真实
// 回测输出上算不出来，或者算出来的量级不对，那这一维就是空的。
//
// 离线是怎么做到的（抄自 pkg/backtest/engine_synthetic_bench_test.go）：
//   - marketdata.NewInMemoryProvider() 顶替行情源
//   - eng.SetRiskManager(rm) 顶替 risk-service —— 引擎的三处 HTTP
//     （仓位 / 择时 / 止损）都有 in-process 分支，接上就不走网络
//
// ⚠️ 这里断言的是**性质**，不是具体数字：行情是合成的，换手率和净收益
// 会随随机种子漂移。硬编码「净收益必须等于 -0.40%」这类断言只会变成
// 日后第一个被删掉的测试。

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/quant-trading/pkg/backtest"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/fees"
	"github.com/ruoxizhnya/quant-trading/pkg/marketdata"
	"github.com/ruoxizhnya/quant-trading/pkg/risk"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy/examples"
	"github.com/spf13/viper"
)

func synthBars(symbols []string, days int, start time.Time, seed int64) map[string][]domain.OHLCV {
	rng := rand.New(rand.NewSource(seed))
	out := make(map[string][]domain.OHLCV, len(symbols))
	for _, sym := range symbols {
		bars := make([]domain.OHLCV, 0, days)
		price := 10.0 + rng.Float64()*90.0
		for d := 0; d < days; d++ {
			open := price
			drift := (rng.Float64() - 0.49) * 0.04
			close := open * (1 + drift)
			vol := float64(1_000_000 + rng.Int63n(5_000_000))
			bars = append(bars, domain.OHLCV{
				Symbol:   sym,
				Date:     start.AddDate(0, 0, d),
				Open:     open,
				High:     math.Max(open, close) * 1.005,
				Low:      math.Min(open, close) * 0.995,
				Close:    close,
				Volume:   vol,
				Turnover: (open + close) / 2 * vol,
			})
			price = close
		}
		out[sym] = bars
	}
	return out
}

// ensureMomentum 保证注册表里有一个按 freq 调仓的 momentum 策略。
//
// 两个坑，都踩过：
//  1. 必须显式 Configure —— NewMomentumStrategy 的 config 是零值，
//     RebalanceFrequency 为空会让 IsRebalanceDay 永远返回 false，
//     于是回测一路跑完但零信号零成交，看起来像成功，其实什么都没测。
//  2. 全局 registry 是单例，第二次 GlobalRegister 会报 already registered。
//     所以注册失败时取出已有实例重新 Configure（不同用例用不同调仓频率）。
type offlineSpec struct {
	nSyms    int
	nDays    int
	topN     int
	lookback int
	seed     int64
	freq     string
}

func ensureMomentum(t *testing.T, spec offlineSpec) {
	t.Helper()
	freq, topN := spec.freq, spec.topN

	params := map[string]interface{}{
		"lookback_days":       spec.lookback,
		"top_n":               topN,
		"max_positions":       topN,
		"rebalance_frequency": freq,
	}

	ms := examples.NewMomentumStrategy()
	if err := ms.Configure(params); err != nil {
		t.Fatalf("configure momentum: %v", err)
	}
	if err := strategy.GlobalRegister(ms); err != nil {
		existing, getErr := strategy.DefaultRegistry.Get("momentum")
		if getErr != nil {
			t.Fatalf("momentum 既注册不上也取不出：register=%v get=%v", err, getErr)
		}
		cfg, ok := existing.(strategy.Configurable)
		if !ok {
			t.Fatal("已注册的 momentum 不可配置，无法切换调仓频率")
		}
		if err := cfg.Configure(params); err != nil {
			t.Fatalf("reconfigure momentum: %v", err)
		}
	}
}

// runOfflineBacktest 用真引擎跑一次回测，不碰数据库、不碰网络。
func runOfflineBacktest(t *testing.T, spec offlineSpec) *domain.BacktestResult {
	t.Helper()
	nSyms, nDays, seed := spec.nSyms, spec.nDays, spec.seed

	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	symbols := make([]string, nSyms)
	for i := range symbols {
		symbols[i] = fmt.Sprintf("%06d.SH", 600000+i)
	}
	data := synthBars(symbols, nDays, start, seed)

	prov := marketdata.NewInMemoryProvider()
	for sym, bars := range data {
		prov.LoadOHLCV(sym, bars)
	}
	days := make([]time.Time, 0, nDays)
	for d := 0; d < nDays; d++ {
		days = append(days, start.AddDate(0, 0, d))
	}
	prov.SetTradingDays(days)
	stocks := make([]domain.Stock, 0, len(symbols))
	for _, sym := range symbols {
		stocks = append(stocks, domain.Stock{Symbol: sym, Name: "S-" + sym, ListDate: start.AddDate(-1, 0, 0)})
	}
	prov.LoadStocks(stocks)

	v := viper.New()
	v.Set("backtest.initial_capital", 1_000_000.0)
	v.Set("backtest.commission_rate", fees.DefaultCommissionRate)
	v.Set("backtest.slippage_rate", fees.DefaultSlippageRate)
	v.Set("backtest.risk_free_rate", 0.03)
	v.Set("backtest.seed", seed)
	v.Set("strategy_service.url", "http://localhost:8082")

	eng, err := backtest.NewEngine(v, prov, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	eng.LoadOHLCVInMemory(data)

	rm, err := risk.NewRiskManager(risk.RiskManagerConfig{
		TargetVolatility: 0.15, MaxPositionWeight: 0.10, MinPositionWeight: 0.01,
		ATRPeriod: 14, BaseMultiplier: 2.0, BullMultiplier: 1.5, BearMultiplier: 3.0,
		SidewaysMultiplier: 2.0, TakeProfitMult: 3.0, VolLookbackDays: 60,
		AnnualizationFactor: math.Sqrt(252), FastMAPeriod: 50, SlowMAPeriod: 200,
		RegimeVolLookback: 120,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("risk.NewRiskManager: %v", err)
	}
	eng.SetRiskManager(rm)

	ensureMomentum(t, spec)

	resp, err := eng.RunBacktest(context.Background(), backtest.BacktestRequest{
		Strategy:       "momentum",
		StockPool:      symbols,
		StartDate:      "2024-01-02",
		EndDate:        start.AddDate(0, 0, nDays-1).Format("2006-01-02"),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	if err != nil {
		t.Fatalf("RunBacktest: %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("回测未完成：status=%s err=%s", resp.Status, resp.Error)
	}
	return &domain.BacktestResult{
		TotalReturn:     resp.TotalReturn,
		AnnualReturn:    resp.AnnualReturn,
		SharpeRatio:     resp.SharpeRatio,
		MaxDrawdown:     resp.MaxDrawdown,
		WinRate:         resp.WinRate,
		TotalTrades:     resp.TotalTrades,
		PortfolioValues: resp.PortfolioValues,
		Trades:          resp.Trades,
	}
}

func TestEconomicIntegration_RealEngineProducesUsableTurnover(t *testing.T) {
	r := runOfflineBacktest(t, offlineSpec{nSyms: 20, nDays: 252, topN: 5, lookback: 20, seed: 42, freq: "daily"})

	if len(r.Trades) == 0 {
		t.Fatal("真引擎没成交 —— 换手率无从算起，这条取证就没意义")
	}

	to, ok := TurnoverFromBacktest(r)
	if !ok {
		t.Fatal("真引擎的完整输出反而算不出换手率")
	}
	if to.AnnualTurnover <= 0 {
		t.Fatalf("有成交就必须有正换手，实际 %.4f", to.AnnualTurnover)
	}
	if to.AvgTradeValue <= 0 {
		t.Fatalf("平均单笔金额应为正，实际 %.2f", to.AvgTradeValue)
	}
	if to.Periods <= 0 {
		t.Fatalf("交易日数应来自市值曲线，实际 %d", to.Periods)
	}

	t.Logf("引擎输出：ret=%.4f sharpe=%.3f trades=%d",
		r.TotalReturn, r.SharpeRatio, r.TotalTrades)
	t.Logf("反推换手：年化单边 %.2f 倍，平均单笔 %.0f 元，%d 个交易日",
		to.AnnualTurnover, to.AvgTradeValue, to.Periods)
}

func TestEconomicIntegration_CostIsRealAgainstEngineOutput(t *testing.T) {
	r := runOfflineBacktest(t, offlineSpec{nSyms: 20, nDays: 252, topN: 5, lookback: 20, seed: 42, freq: "daily"})

	res, ok := ValidateEconomicFromBacktest(r, DefaultAShareCostModel())
	if !ok {
		t.Fatal("真引擎输出跑不出经济校验")
	}

	t.Logf("毛=%.2f%% 成本=%.2f%% 净=%.2f%% 成本占比=%.1f%%",
		res.GrossReturn*100, res.TotalCost*100, res.NetReturn*100, res.CostShare*100)
	t.Logf("年化拖累=%.2f%% 盈亏平衡换手=%.2f 概率=%.3f",
		res.AnnualCostDrag*100, res.BreakEvenTurnover, res.Probability)
	t.Logf("分项：佣金=%.4f 印花税=%.4f 过户费=%.4f 滑点=%.4f",
		res.CostBreakdown.Commission, res.CostBreakdown.StampDuty,
		res.CostBreakdown.TransferFee, res.CostBreakdown.Slippage)

	// 性质 1：扣了费就不可能比不扣更多。
	if res.NetReturn >= res.GrossReturn {
		t.Fatalf("净收益 %.6f 必须严格小于毛收益 %.6f", res.NetReturn, res.GrossReturn)
	}
	if res.TotalCost <= 0 {
		t.Fatal("有成交就必须有成本")
	}

	// 性质 2：分项之和必须等于总成本，否则「钱花在哪」就是编的。
	sum := res.CostBreakdown.Commission + res.CostBreakdown.StampDuty +
		res.CostBreakdown.TransferFee + res.CostBreakdown.Slippage
	if math.Abs(sum-res.TotalCost) > 1e-12 {
		t.Fatalf("成本分项之和 %.10f 与总成本 %.10f 对不上", sum, res.TotalCost)
	}

	// 性质 3（最硬的一条）：BreakEvenTurnover 的定义就是「净收益归零的换手率」，
	// 所以「换手超过盈亏平衡」与「净收益为负」必须同真同假。
	overBreakEven := res.Turnover > res.BreakEvenTurnover
	if overBreakEven != (res.NetReturn < 0) {
		t.Fatalf("盈亏平衡线自相矛盾：换手 %.3f / 平衡 %.3f（超过=%v），净收益 %.6f",
			res.Turnover, res.BreakEvenTurnover, overBreakEven, res.NetReturn)
	}

	// 性质 4：净收益为负时，概率估计必须落在「不太可能成立」的一侧。
	if res.NetReturn < 0 && res.Probability > 0.3 {
		t.Fatalf("净收益 %.4f 为负，概率不该给到 %.3f", res.NetReturn, res.Probability)
	}
}

// 换手率必须对策略行为敏感 —— 换个参数就得换个结果。
// 这条防的是「换手率反推写死成某个常数，策略怎么变它都不动」。
//
// 注意：这里**不能**用 weekly / monthly 做对照。引擎从不设置
// Portfolio.UpdatedAt，于是 momentum 退回到 time.Now() 判断调仓日
// （见 TASKS.md 的 P1-6），weekly 只在「今天是周一」时才发信号 ——
// 回测结果会随运行日期漂移。daily 恒真，才是这里唯一可靠的构造。
func TestEconomicIntegration_TurnoverSensitiveToStrategyParams(t *testing.T) {
	narrow := runOfflineBacktest(t, offlineSpec{nSyms: 20, nDays: 252, topN: 5, lookback: 20, seed: 42, freq: "daily"})
	wider := runOfflineBacktest(t, offlineSpec{nSyms: 20, nDays: 252, topN: 18, lookback: 20, seed: 42, freq: "daily"})

	n, okN := TurnoverFromBacktest(narrow)
	w, okW := TurnoverFromBacktest(wider)
	if !okN || !okW {
		t.Fatal("两个构造都该有成交")
	}

	t.Logf("top_n=5 换手=%.3f（%d 笔） top_n=18 换手=%.3f（%d 笔）",
		n.AnnualTurnover, narrow.TotalTrades, w.AnnualTurnover, wider.TotalTrades)

	if math.Abs(n.AnnualTurnover-w.AnnualTurnover) < 1e-9 {
		t.Fatalf("持仓宽度变了，换手率却纹丝不动（%.6f）—— 换手率反推多半是写死的",
			n.AnnualTurnover)
	}
}
