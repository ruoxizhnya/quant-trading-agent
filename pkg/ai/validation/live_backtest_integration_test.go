package validation

// P2-13 的端到端取证：真库行情 → 真引擎 → 五维验证器链 → 综合裁决。
//
// 与 economic_integration_test.go 的分工：那个用**合成行情**证明「验证器接得上
// 引擎」；这个用库里**真同步下来的行情**证明「接得上，而且真实数据的形状
//（稀疏 / 停牌 / 复权 / 退市 / 上市日期参差）不会把它打崩」。合成数据太干净，
// 证明不了后者 —— 它每一只票都整齐地有 252 根 K 线。
//
// 数据来源：docker compose 的 postgres（库 `quant_trading`），由 cmd/data 的
// `/sync/ohlcv/all` 从 Tushare `daily` + `adj_factor` 落进 `ohlcv_daily_qfq`。
// 库里没数据就 skip —— 那不是代码缺陷，是环境没就绪。
//
// ⚠️ 这一维最容易假绿的地方是**输入**。BiasInput 的字段全部由调用方给，
// 随手填一串好数字就能让偏差维拿高分，而校验器本身没有任何办法发现那些数字
// 是编的。所以这里每一个值都从库里查出来；查不到的宁可留空（→ 记入未评估），
// 也不填一个「应该是这样」的值。

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"github.com/ruoxizhnya/quant-trading/pkg/backtest"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/fees"
	"github.com/ruoxizhnya/quant-trading/pkg/marketdata"
	"github.com/ruoxizhnya/quant-trading/pkg/risk"
	"github.com/ruoxizhnya/quant-trading/pkg/storage"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy"
	"github.com/ruoxizhnya/quant-trading/pkg/strategy/examples"
)

const (
	// livePoolSize 是取证用的股票池规模。
	livePoolSize = 40

	// liveMinBars 是「这只票在区间里有可交易历史」的最低门槛。
	//
	// 故意取得很低（30 根 ≈ 1.5 个月）：门槛一抬高，最先被筛掉的就是
	// **早退市**的票（它们天然 K 线少），于是这一维要查的幸存者偏差
	// 会被取证脚本自己制造出来。000005.SZ（ST星源(退)，2024-04-26 摘牌）
	// 在区间里只有 40 根左右，正好卡在门槛之上 —— 它留在池子里才是证据。
	liveMinBars = 30
)

// liveWindow 是取证区间。库里覆盖 2023-09-25 ~ 2026-09-22，这里掐头去尾，
// 避开同步边界上的稀疏段。
var liveWindow = struct{ Start, End string }{"2024-01-02", "2026-06-30"}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// liveDSN 按**生产同一套** env 命名取连接串。
//
// 不用 testutil.DefaultTestDBConfig：那个的默认库是 `quant_trading_test`，
// 而真同步下来的行情在 `quant_trading` 里 —— 连过去只会得到一个空库，
// 然后测试以「库里没数据」为由 skip，永远绿。这正是「断言落在了不是生产
// 消费的那一层」的另一种形态：连的库不对，测的东西就不是同一份。
func liveDSN(t *testing.T) string {
	t.Helper()
	port := 5432
	if p, err := strconv.Atoi(os.Getenv("DATABASE_PORT")); err == nil && p > 0 {
		port = p
	}
	dsn, err := storage.BuildDSN(storage.DatabaseConfig{
		URL:      os.Getenv("DATABASE_URL"),
		Host:     envOr("DATABASE_HOST", "localhost"),
		Port:     port,
		User:     envOr("DATABASE_USER", "postgres"),
		Password: envOr("DATABASE_PASSWORD", "postgres"),
		Name:     envOr("DATABASE_NAME", "quant_trading"),
		SSLMode:  "disable",
	})
	if err != nil {
		t.Skipf("跳过 P2-13 取证：库连接配置不可用（%v）", err)
	}
	return dsn
}

func openLiveStore(t *testing.T, ctx context.Context) *storage.PostgresStore {
	t.Helper()
	store, err := storage.NewPostgresStore(ctx, liveDSN(t))
	if err != nil {
		t.Skipf("跳过 P2-13 取证：连不上库（%v）。先 `docker compose up -d postgres`", err)
	}
	if err := store.Ping(ctx); err != nil {
		store.Close()
		t.Skipf("跳过 P2-13 取证：库 ping 不通（%v）", err)
	}
	t.Cleanup(store.Close)
	return store
}

// livePool 选股票池。
//
// **按代码顺序取**，不按「历史完整度」排序。按完整度排是在用未来信息挑样本
// （排在前面的必然是活到今天的票），等于把这一维要查的幸存者偏差自己造出来。
// 按代码顺序取会自然带进退市票：000004.SZ / 000005.SZ 就在池子里，
// 这正是 survivorship 子维度需要的证据。
func livePool(t *testing.T, ctx context.Context, store *storage.PostgresStore) []string {
	t.Helper()
	rows, err := store.DB().Query(ctx, `
		SELECT symbol
		FROM ohlcv_daily_qfq
		WHERE trade_date >= $1::date AND trade_date <= $2::date
		GROUP BY symbol
		HAVING count(*) >= $3
		ORDER BY symbol
		LIMIT $4
	`, liveWindow.Start, liveWindow.End, liveMinBars, livePoolSize)
	if err != nil {
		t.Fatalf("查股票池失败：%v", err)
	}
	defer rows.Close()

	var pool []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("扫 symbol 失败：%v", err)
		}
		pool = append(pool, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历股票池出错：%v", err)
	}
	return pool
}

// delistedInPool 数池子里在区间内摘牌的票 —— survivorship 子维度最直接的证据。
// 池子里一只退市票都没有，十有八九是按今天的名单取的。
func delistedInPool(t *testing.T, ctx context.Context, store *storage.PostgresStore, pool []string) int {
	t.Helper()
	var n int
	err := store.DB().QueryRow(ctx, `
		SELECT count(*)
		FROM stocks
		WHERE symbol = ANY($1)
		  AND delist_date IS NOT NULL
		  AND delist_date >= $2::date
		  AND delist_date <= $3::date
	`, pool, liveWindow.Start, liveWindow.End).Scan(&n)
	if err != nil {
		t.Fatalf("查退市票失败：%v", err)
	}
	return n
}

// configureMomentum 让注册表里的 momentum 用给定参数。
//
// 全局 registry 是单例，第二次 GlobalRegister 会报 already registered，
// 所以注册失败时取出已有实例重新 Configure（同 economic_integration_test.go）。
func configureMomentum(t *testing.T, params map[string]interface{}) {
	t.Helper()
	ms := examples.NewMomentumStrategy()
	if err := strategy.GlobalRegister(ms); err != nil {
		existing, getErr := strategy.DefaultRegistry.Get("momentum")
		if getErr != nil {
			t.Fatalf("momentum 既注册不上也取不出：register=%v get=%v", err, getErr)
		}
		cfg, ok := existing.(strategy.Configurable)
		if !ok {
			t.Fatal("已注册的 momentum 不可配置")
		}
		if err := cfg.Configure(params); err != nil {
			t.Fatalf("reconfigure momentum: %v", err)
		}
		return
	}
	if err := ms.Configure(params); err != nil {
		t.Fatalf("configure momentum: %v", err)
	}
}

// liveSpec 是一次取证的构造参数。
type liveSpec struct {
	pool     []string
	lookback int
	topN     int
}

// runLiveBacktest 用真 provider + 真库跑一次回测。不碰网络（risk 走 in-process）。
func runLiveBacktest(t *testing.T, ctx context.Context, store *storage.PostgresStore, spec liveSpec) *domain.BacktestResult {
	t.Helper()

	prov := marketdata.NewPostgresProvider(store, zerolog.Nop())

	v := viper.New()
	v.Set("backtest.initial_capital", 1_000_000.0)
	v.Set("backtest.commission_rate", fees.DefaultCommissionRate)
	v.Set("backtest.slippage_rate", fees.DefaultSlippageRate)
	v.Set("backtest.risk_free_rate", 0.03)
	v.Set("backtest.seed", 7)
	v.Set("strategy_service.url", "http://localhost:8082")

	eng, err := backtest.NewEngine(v, prov, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

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

	configureMomentum(t, map[string]interface{}{
		"lookback_days":       spec.lookback,
		"top_n":               spec.topN,
		"max_positions":       spec.topN,
		"rebalance_frequency": "daily",
	})

	resp, err := eng.RunBacktest(ctx, backtest.BacktestRequest{
		Strategy:       "momentum",
		StockPool:      spec.pool,
		StartDate:      liveWindow.Start,
		EndDate:        liveWindow.End,
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	if err != nil {
		t.Fatalf("RunBacktest（真库 %s~%s，%d 只票）：%v",
			liveWindow.Start, liveWindow.End, len(spec.pool), err)
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
		// OBS-01：把引擎算出的票池统计一并带过来，验证器的「票池非空」维
		// 才读得到真实的非空票池（否则会被误判成无效运行）。
		UniverseMaxSize: resp.UniverseMaxSize,
	}
}

// TestValidatorChain_AgainstLiveDatabase 是 P2-13 的验收本体。
//
// 链条：真库行情 → 真引擎回测（含真实退市票）→ 五维验证器 → 综合裁决。
// 因果维（第六维）**故意不评估** —— 它要一次 LLM 调用，而这一维是确定性的，
// 不该为了凑齐六维把不确定的东西拉进来。
func TestValidatorChain_AgainstLiveDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	store := openLiveStore(t, ctx)

	pool := livePool(t, ctx, store)
	if len(pool) == 0 {
		t.Skipf("跳过 P2-13 取证：%s~%s 区间里没有任何票有 >=%d 根 K 线 —— "+
			"先跑一次 `/sync/ohlcv/all` 把行情补齐", liveWindow.Start, liveWindow.End, liveMinBars)
	}

	// 前置条件断言：池子不够大就不该往下跑。假绿最爱藏在这里 ——
	// 池子只剩 2 只票时回测照样「completed」，结论却毫无意义。
	if len(pool) < 20 {
		t.Fatalf("池子只有 %d 只票（要 >=20）—— 库里数据还太薄，这次取证不成立。"+
			"这不是测试失败，是数据前置没满足，但它不该被 skip 掉：skip 会让人以为跑过了。",
			len(pool))
	}

	delisted := delistedInPool(t, ctx, store, pool)
	// 这一条是**取证脚本自身的体检**：如果池子里一只退市票都没有，
	// 那这个池子就是幸存者选出来的，后面 survivorship 子维度的高分不可信。
	if delisted == 0 {
		t.Fatalf("池子里 %d 只票没有一只在区间内摘牌 —— 池子被幸存者选过，"+
			"这一维的结论不成立（检查 stocks.delist_date 是否同步过）", len(pool))
	}

	// 参数扫描：三个 lookback 既给出真实邻域（稳健维要），
	// 又让 NumTrials 是一个**真实发生过的**次数，而不是编一个好看的数。
	lookbacks := []int{15, 20, 25}
	results := make(map[int]*domain.BacktestResult, len(lookbacks))
	var neighbors []NeighborPoint
	for _, lb := range lookbacks {
		r := runLiveBacktest(t, ctx, store, liveSpec{pool: pool, lookback: lb, topN: 5})
		results[lb] = r
		neighbors = append(neighbors, NeighborPoint{
			Params: map[string]interface{}{"lookback_days": lb, "top_n": 5},
			Sharpe: r.SharpeRatio,
			Return: r.TotalReturn,
		})
	}

	center := results[20]
	if center == nil || len(center.Trades) == 0 {
		t.Fatalf("真库回测零成交 —— 后面每一维都无从算起，这次取证没意义")
	}

	// 冗余维的「已有组合」：同策略换一组参数。相关性必然很高 ——
	// 那正是这一维要暴露的「伪分散」形态，用它证明这一维真的在报警而不是摆设。
	existing := map[string]*domain.BacktestResult{
		"momentum_wide": runLiveBacktest(t, ctx, store, liveSpec{pool: pool, lookback: 60, topN: 12}),
	}

	biasIn := BiasInput{
		// 复权口径：真库落的表就叫 ohlcv_daily_qfq（前复权）。
		// 这里如实写 AdjustPre —— 它会拿到 0.85 并附带「前复权自带前视成分」的提醒，
		// 而不是被填成 AdjustPost 骗一个 0.9（P2-8 要复核的正是这件事）。
		PriceAdjustment: AdjustPre,

		// 池子来源：引擎的 eligibleUniverse 按上市/摘牌日历逐日过滤（P2-4），
		// 这是 point-in-time 的定义。
		PoolSource: PoolSourcePointInTime,
		PoolSize:   len(pool),
		// 退市票数量从库里查出来，不是填的。
		DelistedInPool: delisted,

		// 前视：**留空**。这次跑的 momentum 只吃价量，而「引擎逐日喂 K 线、
		// 不预读未来 bar」这件事本测试没有独立验证过，写 PITVerified=true
		// 就是盖一个没做过的章。留空 → 这一子维度记入未评估，如实暴露。
	}

	verdict := ValidateProposal(Proposal{
		Name:         fmt.Sprintf("momentum/lb20 over live DB %s..%s", liveWindow.Start, liveWindow.End),
		Result:       center,
		NumTrials:    len(lookbacks),
		FailedTrials: 0,
		Neighbors:    neighbors,
		Cost:         DefaultAShareCostModel(),
		Bias:         biasIn,
		Existing:     existing,
		Causal:       nil, // 见函数头：故意不评估
	})

	// ---- 断言 ----

	// 1) 五个确定性维度全部评估到了（因果维不在其中）。
	want := []string{DimensionStatistical, DimensionEconomic, DimensionRobustness,
		DimensionBias, DimensionRedundancy}
	var missing []string
	for _, d := range want {
		if _, ok := verdict.Dimensions[d]; !ok {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("真库跑出来的回测结果喂进去，这几维却没评估：%v（未评估=%v）",
			missing, verdict.Unassessed)
	}

	// 2) 因果维必须**如实地**记为未评估，而不是悄悄漏掉。
	foundCausal := false
	for _, u := range verdict.Unassessed {
		if u == DimensionCausal {
			foundCausal = true
		}
	}
	if !foundCausal {
		t.Fatalf("没有喂 Causal 却也没记进未评估 —— 「没查」被藏起来了：%v", verdict.Unassessed)
	}

	// 3) 综合概率 = 已评估维度的最小值（聚合器的定义）。
	//    这条不是抄实现：它是「结论受限于最弱一环」这个立场的可执行形式。
	minP, weakest := 1.0, ""
	for d, p := range verdict.Dimensions {
		if p < minP {
			minP, weakest = p, d
		}
	}
	if verdict.Probability != minP {
		t.Fatalf("综合概率 %.6f 不等于最弱维（%s=%.6f）—— 聚合口径被改动了",
			verdict.Probability, weakest, minP)
	}
	if verdict.Weakest != weakest {
		t.Fatalf("Weakest 报的是 %q，实际最弱是 %q", verdict.Weakest, weakest)
	}

	// 4) 偏差维：**真实**的复权口径与退市票必须出现在质疑清单里。
	//    这两条才是 P2-13 的取证价值 —— 它们证明喂进去的是真库的事实，
	//    而不是一串恰好让校验器满意的数字。
	if verdict.BiasResult == nil {
		t.Fatal("偏差维没结果")
	}
	if verdict.BiasResult.Survivorship == 0 {
		t.Fatal("池子里有真退市票，survivorship 却未评估")
	}
	var sawDelistedNote, sawAdjustNote bool
	for _, c := range verdict.Challenges {
		if c.Dimension != DimensionBias {
			continue
		}
		if c.Severity == SeverityNote && strings.Contains(c.Message, "退市") {
			sawDelistedNote = true
		}
		if strings.Contains(c.Message, "复权") {
			sawAdjustNote = true
		}
	}
	if !sawDelistedNote {
		t.Fatalf("池子里真有 %d 只退市票，质疑清单却没提 —— 校验器没看见这份证据", delisted)
	}
	if !sawAdjustNote {
		t.Fatal("行情是前复权（自带前视成分），质疑清单却没提 —— 这个已知债被吞了")
	}

	t.Logf("池子：%d 只（其中区间内摘牌 %d 只）", len(pool), delisted)
	t.Logf("回测：ret=%.4f sharpe=%.3f mdd=%.4f trades=%d",
		center.TotalReturn, center.SharpeRatio, center.MaxDrawdown, center.TotalTrades)
	t.Logf("综合概率=%.4f 最弱维=%s 几何平均=%.4f blocking=%d",
		verdict.Probability, verdict.Weakest, verdict.GeometricMean, verdict.Blocking)
	for d, p := range verdict.Dimensions {
		t.Logf("  %-12s %.4f", d, p)
	}
	t.Logf("未评估：%v", verdict.Unassessed)
	for _, c := range verdict.Challenges {
		t.Logf("  [%s/%s] %s", c.Dimension, c.Severity, c.Message)
	}
}
