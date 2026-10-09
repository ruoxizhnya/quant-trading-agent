package yaml

import (
	"context"
	"testing"
	"time"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/intent"
	"github.com/ruoxizhnya/quant-trading/pkg/domain"
	"github.com/ruoxizhnya/quant-trading/pkg/expression"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P0-5 的第二层根因：意图 → 可执行配置之间缺一层映射。
//
// Generator 只在 intent 自带 `signal_expr` 参数时才产出 expression 段；
// 规则解析（ruleBasedExtract）出来的 intent 只带语义类型（momentum /
// mean_reversion / ...），没有 signal_expr，于是 LoadStrategy 直接拒绝
// —— 它只认 expression 类型。链路就断在这里。
//
// 修法：给「能用价量表达」的意图类型一个确定性的默认表达式。映射写在
// 生成的 YAML 里，人能审阅、能改参数；AI 后续调的是旋钮，不是每次
// 重新发明一个策略。

func TestGenerate_PriceExpressibleTypesProduceLoadableStrategy(t *testing.T) {
	cases := []struct {
		name string
		typ  intent.StrategyType
	}{
		{"momentum", intent.StrategyTypeMomentum},
		{"mean_reversion", intent.StrategyTypeMeanReversion},
		{"trend_following", intent.StrategyTypeTrendFollowing},
		{"breakout", intent.StrategyTypeBreakout},
		{"multi_factor", intent.StrategyTypeMultiFactor},
	}

	g := NewGenerator()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := &intent.Intent{
				StrategyName: "test_" + tc.name,
				StrategyType: tc.typ,
				Universe:     "csi300",
				Timeframe:    "1d",
			}
			yamlStr := g.Generate(i)
			require.NotEmpty(t, yamlStr)

			s, err := LoadStrategy(yamlStr)
			require.NoError(t, err, "生成的 YAML 必须能加载成可执行策略")
			assert.Equal(t, "test_"+tc.name, s.Name())

			cfg, err := ParseConfig(yamlStr)
			require.NoError(t, err)
			require.NotEmpty(t, cfg.Expression.Signal.Expression,
				"没有表达式就没有可执行内容")

			_, err = expression.NewParser().Parse(cfg.Expression.Signal.Expression)
			assert.NoError(t, err, "默认表达式必须能被表达式引擎解析")
		})
	}
}

// TestGenerate_FundamentalTypesProduceRealExpressions：P2-12 之后，
// pe / pb / ps / roe / roa 进了表达式引擎，value / quality 终于能给出
// 诚实表达式了 —— 不再需要「明确失败」这条退路。
//
// 「绝不拿价格糊弄」的保证没有消失，只是挪了地方：现在由
// OHLCVDataProvider 守着 —— 没有财报数据时用 pe 会报错，而不是返回 0
// （见 expression 包的 TestGetField_FundamentalField_Error
// 与 TestExpressionStrategy_ValueExprFailsWithoutFundamentals）。
func TestGenerate_FundamentalTypesProduceRealExpressions(t *testing.T) {
	cases := []struct {
		typ      intent.StrategyType
		wantRefs []string // 表达式里必须出现的字段
		mustNot  string   // 绝不能用这个糊弄
	}{
		{intent.StrategyTypeValue, []string{"pe", "pb"}, ""},
		{intent.StrategyTypeQuality, []string{"roe", "roa"}, ""},
	}

	for _, tc := range cases {
		t.Run(string(tc.typ), func(t *testing.T) {
			i := &intent.Intent{
				StrategyName: "test_" + string(tc.typ),
				StrategyType: tc.typ,
				Universe:     "csi300",
				Timeframe:    "1d",
			}
			yamlStr := NewGenerator().Generate(i)

			s, err := LoadStrategy(yamlStr)
			require.NoError(t, err, "value/quality 现在必须能加载成策略")

			cfg, err := ParseConfig(yamlStr)
			require.NoError(t, err)
			expr := cfg.Expression.Signal.Expression
			require.NotEmpty(t, expr)

			for _, ref := range tc.wantRefs {
				assert.Contains(t, expr, ref, "表达式要真的用到 %s，不能拿价格糊弄", ref)
			}
			_, err = expression.NewParser().Parse(expr)
			assert.NoError(t, err, "默认表达式必须能被表达式引擎解析")
			_ = s
		})
	}
}

// TestGenerate_CustomStillFailsExplicitly：custom 依然无解 —— 它没有语义，
// 编不出诚实表达式，宁可报错也不给默认值。
func TestGenerate_CustomStillFailsExplicitly(t *testing.T) {
	i := &intent.Intent{
		StrategyName: "test_custom",
		StrategyType: intent.StrategyTypeCustom,
		Universe:     "csi300",
		Timeframe:    "1d",
	}
	_, err := LoadStrategy(NewGenerator().Generate(i))
	require.Error(t, err, "custom 没有语义，必须明确失败而不是随便给个表达式")
}

// series 造一段价格序列。默认表达式用的是 20 日窗口，所以期数要够。
func series(symbol string, n int, price func(i int) float64) []domain.OHLCV {
	bars := make([]domain.OHLCV, 0, n)
	base := time.Now().AddDate(0, 0, -n)
	for i := 0; i < n; i++ {
		p := price(i)
		bars = append(bars, domain.OHLCV{
			Symbol: symbol,
			Date:   base.AddDate(0, 0, i),
			Open:   p, High: p, Low: p, Close: p, Volume: 1000,
		})
	}
	return bars
}

// trendBars 造三只走势分明的股票：一路涨 / 一路跌 / 横盘。
func trendBars() map[string][]domain.OHLCV {
	const n = 60
	return map[string][]domain.OHLCV{
		"RISER":  series("RISER", n, func(i int) float64 { return 10 + float64(i)*0.5 }),
		"FALLER": series("FALLER", n, func(i int) float64 { return 40 - float64(i)*0.5 }),
		"FLAT":   series("FLAT", n, func(i int) float64 { return 20 }),
	}
}

// TestDefaultExpressions_ProduceSensibleSignals：语法合法不等于语义有效。
// 默认表达式必须真能算出信号，而且方向要对 —— 动量买涨得最猛的，
// 均值回归买跌得最深的。两个方向相反的断言一起，能挡住「表达式写反了」。
func TestDefaultExpressions_ProduceSensibleSignals(t *testing.T) {
	cases := []struct {
		name     string
		typ      intent.StrategyType
		wantOnly string
	}{
		{"momentum_buys_the_riser", intent.StrategyTypeMomentum, "RISER"},
		{"mean_reversion_buys_the_faller", intent.StrategyTypeMeanReversion, "FALLER"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := &intent.Intent{StrategyName: "sig_" + tc.name, StrategyType: tc.typ}
			s, err := LoadStrategy(NewGenerator().Generate(i))
			require.NoError(t, err)

			signals, err := s.GenerateSignals(context.Background(), trendBars(), nil)
			require.NoError(t, err)
			require.NotEmpty(t, signals, "默认表达式必须产出信号，不能是个摆设")

			symbols := make([]string, 0, len(signals))
			for _, sig := range signals {
				symbols = append(symbols, sig.Symbol)
			}
			assert.Equal(t, []string{tc.wantOnly}, symbols,
				"选中的标的应该只有 %s，实际是 %v", tc.wantOnly, symbols)
		})
	}
}

// TestGenerate_ExplicitSignalExprWins：显式指定的表达式优先于类型默认。
func TestGenerate_ExplicitSignalExprWins(t *testing.T) {
	i := &intent.Intent{
		StrategyName: "explicit",
		StrategyType: intent.StrategyTypeMomentum,
		Parameters:   []intent.Parameter{{Name: "signal_expr", Value: "cs_rank(volume) > 0.9"}},
	}
	cfg, err := ParseConfig(NewGenerator().Generate(i))
	require.NoError(t, err)
	assert.Equal(t, "cs_rank(volume) > 0.9", cfg.Expression.Signal.Expression)
}
