package validation

import (
	"context"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// 因果维的取证。
//
// 这一维最容易做假：让模型事后讲个故事，看起来就"通过"了。所以测试守的
// 不是"能不能讲出机制"，而是三件事：
//  1. 预测**在看到结果之前**做出（叙述者拿不到回测结果）
//  2. 讲错会被确定性检验打脸（预测不中 = blocking）
//  3. 不下注（没边界 / 验不了）不算通过

func fptr(v float64) *float64 { return &v }

// causalEquityCurve 造一条净值曲线：前 n-1 天小幅上涨，最后一天大涨。
// 最后一天那一下用来验证「收益集中度」这类预测算得对。
func causalEquityCurve(n int, daily float64, lastDayJump float64) []domain.PortfolioValue {
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	out := make([]domain.PortfolioValue, n)
	eq := 1_000_000.0
	for i := range out {
		if i > 0 {
			if i == n-1 {
				eq *= 1 + lastDayJump
			} else {
				eq *= 1 + daily
			}
		}
		out[i] = domain.PortfolioValue{Date: start.AddDate(0, 0, i), TotalValue: eq}
	}
	return out
}

func sampleResult() *domain.BacktestResult {
	return &domain.BacktestResult{
		SharpeRatio:     1.2,
		TotalReturn:     0.35,
		TotalTrades:     40,
		WinTrades:       24,
		LoseTrades:      16,
		WinRate:         0.6,
		AvgHoldingDays:  12,
		MaxDrawdown:     -0.18,
		PortfolioValues: causalEquityCurve(300, 0.0005, 0.05),
		// OBS-01：正常回测的票池非空 —— 见 aggregate_test.go resultFixture。
		UniverseMaxSize: intPtr(40),
		Trades: []domain.Trade{{
			Symbol: "600000.SH", Direction: domain.DirectionLong,
			Quantity: 1000, Price: 50, Timestamp: time.Now(),
		}},
	}
}

// stubNarrator 按脚本返回理论，并记录它收到的请求 ——
// 记录请求是为了验证「结果没被递给模型」这条硬规矩。
type stubNarrator struct {
	theory *CausalTheory
	err    error
	gotReq CausalRequest
	called bool
	// leakResult 为 true 时故意把结果塞进请求（模拟错误接线），
	// 用于证明测试真的能发现这件事。
	leakResult bool
}

func (s *stubNarrator) Narrate(ctx context.Context, req CausalRequest) (*CausalTheory, error) {
	s.called = true
	s.gotReq = req
	if s.leakResult {
		req.Result = &domain.BacktestResult{SharpeRatio: 1.2}
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.theory, nil
}

func TestValidateCausal_NarratorNilIsUnassessed(t *testing.T) {
	got, err := ValidateCausal(context.Background(), CausalRequest{Result: sampleResult()}, nil)
	if err != nil {
		t.Fatalf("叙述者为 nil 不该报错: %v", err)
	}
	if got != nil {
		t.Fatalf("叙述者为 nil 时应返回 nil（= 未评估），实际 %+v", got)
	}
}

// 叙述者看不到回测结果 —— 这是这一维的立身之本。
// 知道答案之后做的"预测"只是复述，那样的因果维会永远通过。
func TestValidateCausal_NarratorNeverSeesResult(t *testing.T) {
	n := &stubNarrator{theory: &CausalTheory{Mechanism: "m"}}
	if _, err := ValidateCausal(context.Background(),
		CausalRequest{Result: sampleResult(), Params: map[string]any{"lookback_days": 20}}, n); err != nil {
		t.Fatalf("ValidateCausal: %v", err)
	}
	if !n.called {
		t.Fatal("叙述者没被调用")
	}
	if n.gotReq.Result != nil {
		t.Fatal("回测结果被递给了叙述者 —— 那样它只会照着结果反推机制")
	}
	// 描述性上下文该给的要给到，否则模型无从下注。
	if n.gotReq.Params["lookback_days"] != 20 {
		t.Fatalf("参数没传下去: %v", n.gotReq.Params)
	}
}

// 讲对了：预测应验，概率应按后验均值走，且不该有 blocking。
func TestValidateCausal_PredictionsVerified(t *testing.T) {
	n := &stubNarrator{theory: &CausalTheory{
		Mechanism: "动量：涨得好的票短期继续涨",
		Predictions: []Prediction{
			{Kind: PredictionWinRate, Statement: "胜率应过半", Min: fptr(0.5)},
			{Kind: PredictionHoldingDays, Statement: "持有应在 5~30 天", Min: fptr(5), Max: fptr(30)},
			{Kind: PredictionMaxDrawdown, Statement: "回撤应在 30% 以内", Max: fptr(0.30)},
		},
	}}

	got, err := ValidateCausal(context.Background(), CausalRequest{Result: sampleResult()}, n)
	if err != nil {
		t.Fatalf("ValidateCausal: %v", err)
	}
	if got.Testable != 3 || got.Passed != 3 || got.Failed != 0 {
		t.Fatalf("三条都应验，实际 testable=%d passed=%d failed=%d", got.Testable, got.Passed, got.Failed)
	}
	// Beta(1,1) 后验：(3+1)/(3+2) = 0.8
	if got.Probability < 0.79 || got.Probability > 0.81 {
		t.Fatalf("概率应为 0.80，实际 %.3f", got.Probability)
	}
	for _, c := range got.Challenges {
		if c.Severity == SeverityBlocking {
			t.Fatalf("全中的预测不该有 blocking 质疑: %s", c.Message)
		}
	}
}

// 讲错了：预测被证伪必须是 blocking —— 机制和数据对不上，这是硬证据。
func TestValidateCausal_FailedPredictionIsBlocking(t *testing.T) {
	n := &stubNarrator{theory: &CausalTheory{
		Mechanism: "短线反转",
		Predictions: []Prediction{
			{Kind: PredictionHoldingDays, Statement: "应当日内进出（<2 天）", Max: fptr(2)},
			{Kind: PredictionWinRate, Statement: "胜率应过半", Min: fptr(0.5)},
		},
	}}

	got, err := ValidateCausal(context.Background(), CausalRequest{Result: sampleResult()}, n)
	if err != nil {
		t.Fatalf("ValidateCausal: %v", err)
	}
	if got.Failed != 1 || got.Passed != 1 {
		t.Fatalf("应 1 中 1 不中，实际 passed=%d failed=%d", got.Passed, got.Failed)
	}
	var blocking bool
	for _, c := range got.Challenges {
		if c.Severity == SeverityBlocking {
			blocking = true
		}
	}
	if !blocking {
		t.Fatal("预测被证伪却没有 blocking 质疑")
	}
	// 押两条中一条：(1+1)/(2+2) = 0.5
	if got.Probability < 0.49 || got.Probability > 0.51 {
		t.Fatalf("概率应为 0.50，实际 %.3f", got.Probability)
	}
}

// 只在嘴上讲故事：要么没给边界，要么这个量根本算不出来。
// 两种情况都**不能**算通过 —— 讲得出但不下注，等于没讲。
func TestValidateCausal_UnfalsifiableIsNotPassing(t *testing.T) {
	n := &stubNarrator{theory: &CausalTheory{
		Mechanism: "某种市场结构",
		Predictions: []Prediction{
			{Kind: PredictionWinRate, Statement: "胜率应该还行"},                    // 没边界
			{Kind: "moon_phase", Statement: "月相也该配合", Min: fptr(0.1)},         // 未知种类
			{Kind: PredictionTradeCount, Statement: "交易次数 < 5", Max: fptr(5)}, // 可验，会不中
		},
	}}

	got, err := ValidateCausal(context.Background(), CausalRequest{Result: sampleResult()}, n)
	if err != nil {
		t.Fatalf("ValidateCausal: %v", err)
	}
	if got.Testable != 1 {
		t.Fatalf("只有第三条可证伪，实际 testable=%d", got.Testable)
	}
	if got.Passed != 0 || got.Failed != 1 {
		t.Fatalf("可验的那条应不中，实际 passed=%d failed=%d", got.Passed, got.Failed)
	}
	for _, pr := range got.Predictions[:2] {
		if pr.Verifiable {
			t.Fatalf("这条本该无法检验: %+v", pr)
		}
		if pr.Note == "" {
			t.Fatal("无法检验必须说清为什么，不能默默跳过")
		}
	}
}

// 一条可证伪的预测都没有：讲了散文，不是理论。
func TestValidateCausal_NoTestablePredictionsScoresLow(t *testing.T) {
	n := &stubNarrator{theory: &CausalTheory{
		Mechanism: "市场是有效的，除了它无效的时候",
		Predictions: []Prediction{
			{Kind: PredictionWinRate, Statement: "胜率会不错"},
		},
	}}

	got, err := ValidateCausal(context.Background(), CausalRequest{Result: sampleResult()}, n)
	if err != nil {
		t.Fatalf("ValidateCausal: %v", err)
	}
	if got.Probability > 0.3 {
		t.Fatalf("一条可证伪的都没有，概率应很低，实际 %.3f", got.Probability)
	}
	var blocking bool
	for _, c := range got.Challenges {
		if c.Severity == SeverityBlocking {
			blocking = true
		}
	}
	if !blocking {
		t.Fatal("只讲故事不下注，应当被 blocking")
	}
}

// 收益集中度：最后一天暴涨的策略，收益应明显集中 ——
// 这正是「机制若是日频动量，收益不该来自某一天」这类预测要抓的东西。
//
// 绝对值没那么重要（判"算不算集中"是模型下注时给的边界说了算），
// 要紧的是它**能分辨**：集中 vs 均匀得差出一个量级。
func TestTopKReturnShare(t *testing.T) {
	// 300 天里每天 +0.05%，最后一天 +5%（是日常的 100 倍）。
	spiky, ok := topKReturnShare(causalEquityCurve(300, 0.0005, 0.05), 5)
	if !ok {
		t.Fatal("正收益序列应算得出集中度")
	}
	// 均匀上涨 → 5/300 约等于 1.7%。
	flat, ok := topKReturnShare(causalEquityCurve(300, 0.001, 0.001), 5)
	if !ok {
		t.Fatal("正收益序列应算得出集中度")
	}
	if spiky < flat*5 {
		t.Fatalf("集中度分辨不出来：尖峰 %.3f vs 均匀 %.3f", spiky, flat)
	}
	t.Logf("收益集中度：尖峰 %.3f（最后一天 +5%%），均匀 %.3f", spiky, flat)

	// 总收益为负时算不出集中度 —— 那时该问的是为什么会亏。
	if _, ok := topKReturnShare(causalEquityCurve(300, -0.001, -0.001), 5); ok {
		t.Fatal("总收益为负时不该给出集中度")
	}
}

// AttachCausal：因果维是事后补的（每次尝试都跑 LLM 太贵），
// 补进来之后综合概率和最弱维必须重算，否则它等于没进这条链。
func TestAttachCausal_RebuildsVerdict(t *testing.T) {
	p := Proposal{
		Result:    sampleResult(),
		NumTrials: 3,
		Bias:      BiasInput{PriceAdjustment: AdjustPre},
	}
	v := ValidateProposal(p)

	if !containsString(v.Unassessed, DimensionCausal) {
		t.Fatalf("没跑因果维时应记成未评估，实际 %v", v.Unassessed)
	}

	// 一个"讲不通"的因果结果：概率 0.2，低于其余各维，应当接管最弱维。
	// （各维当前是 统计 0.53 / 经济 1 / 稳健 1 / 偏差 0.85。）
	AttachCausal(&v, &CausalResult{Probability: 0.2, Testable: 3, Passed: 0, Failed: 3})

	if containsString(v.Unassessed, DimensionCausal) {
		t.Fatalf("补上之后不该还记着未评估: %v", v.Unassessed)
	}
	if v.Causal == nil {
		t.Fatal("Causal 结果没挂上")
	}
	if v.Dimensions[DimensionCausal] != 0.2 {
		t.Fatalf("因果维概率未进 Dimensions: %v", v.Dimensions)
	}
	// 综合概率取各维最小值：补进来之后最弱的一环就是它。
	if v.Weakest != DimensionCausal {
		t.Fatalf("补进来之后最弱维应是 causal，实际 %s（dimensions=%v）", v.Weakest, v.Dimensions)
	}
	if v.Probability != 0.2 {
		t.Fatalf("综合概率应被因果维拉到 0.2，实际 %.3f", v.Probability)
	}
	for _, c := range v.Challenges {
		if c.Dimension == DimensionCausal && containsUnassessedCausal(c.Message) {
			t.Fatalf("还留着「未评估」的 note，页面上会自相矛盾: %s", c.Message)
		}
	}
	// 重算而不是累加 blocking —— 否则补一次质疑数就翻倍。
	before := v.Blocking
	AttachCausal(&v, &CausalResult{Probability: 0.2, Testable: 3, Passed: 0, Failed: 3})
	if v.Blocking != before {
		t.Fatalf("重复 Attach 不该改变 blocking 数：%d → %d", before, v.Blocking)
	}
}
