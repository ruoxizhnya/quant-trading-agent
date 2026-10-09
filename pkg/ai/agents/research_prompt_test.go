package agents

import (
	"strings"
	"testing"

	"github.com/ruoxizhnya/quant-trading/pkg/expression"
)

// TestFactorDSLSyntax_DerivesFromRegistry 是 OBS-08 的护栏：研究提示词里
// 广告的字段与算子清单必须与 pkg/expression 注册表一致。
//
// 这条护栏的由来：2026-10-09 实测该段曾硬编码，广告了 `market_cap`
// （provider 永不可供的幻影字段 ⇒ AI 产出必然过不了闸门的表达式），
// 同时漏掉 `ps`/`roa`/`revenue`/`profit` 与 9 个算子。
// 现在还加了反向断言：已从注册表移除的幻影字段不得再被广告。
func TestFactorDSLSyntax_DerivesFromRegistry(t *testing.T) {
	// nil = 未做可用性探测，退回能力层全集（保持本护栏原有的「不漏」语义）。
	got := factorDSLSyntax(nil)

	for _, f := range expression.AvailableDataFields() {
		if !strings.Contains(got, f) {
			t.Errorf("提示词缺少可用字段 %q（注册表有、提示词没广告 ⇒ AI 少用能力）", f)
		}
	}
	for _, op := range expression.AvailableOperators() {
		if !strings.Contains(got, op) {
			t.Errorf("提示词缺少算子 %q（注册表有、提示词没广告 ⇒ AI 少用能力）", op)
		}
	}

	// 反向：这些字段当前 provider 永不可供，提示词不得再广告它们。
	// 注意 `volatility` 不在此列 —— 它是 factor 的 *category* 名，不是 DSL 字段。
	for _, phantom := range []string{"market_cap", "roe_ttm", "eps", "vwap", "turnover_rate"} {
		if strings.Contains(got, phantom) {
			t.Errorf("提示词广告了不存在的字段 %q（OBS-08：AI 会产出必然过不了闸门的表达式）", phantom)
		}
	}
}

// TestFactorDSLSyntax_MarksUnavailableFields 是 OBS-08 切片 2 的落点护栏：
// 源表为空的字段（实测 stock_fundamentals / stock_sector_map 均 0 行）必须
// ① 不出现在「可用字段」清单里，② 被明确标注为「当前无数据、别用」。
//
// 只做 ① 不够：AI 会因为「记得 DSL 里有 pe/roe」而反复尝试，等于把一轮试验
// 浪费在注定拿不到数据的公式上（危害被 OBS-01 兜住，但纯属白跑）。
func TestFactorDSLSyntax_MarksUnavailableFields(t *testing.T) {
	// 模拟真库现状：只有行情有数据。
	avail := map[string]bool{
		"open": true, "high": true, "low": true,
		"close": true, "volume": true, "turnover": true,
		"pe": false, "pb": false, "ps": false, "roe": false,
		"roa": false, "revenue": false, "profit": false,
		"sector": false,
	}
	got := factorDSLSyntax(avail)

	// ① 「Data fields:」那一行只含可用字段。
	//
	// 用**精确字段集合**而非 strings.Contains：实测 Contains("pe") 会命中
	// "open"（o-pe-n）这种子串，把正确输出误判成错误（与「按行位置判断」
	// 同类的脆弱断言）。
	dataLine := lineStartingWith(got, "- Data fields:")
	if dataLine == "" {
		t.Fatal("提示词缺少 Data fields 行")
	}
	listed := fieldSet(dataLine)
	for _, f := range []string{"pe", "pb", "ps", "roe", "roa", "revenue", "profit"} {
		if listed[f] {
			t.Errorf("零数据的字段 %q 出现在可用清单里：%s", f, dataLine)
		}
	}
	for _, f := range []string{"close", "volume"} {
		if !listed[f] {
			t.Errorf("有数据的字段 %q 未在可用清单里：%s", f, dataLine)
		}
	}

	// ② 不可用字段必须被明确点名禁用。
	unusableLine := lineStartingWith(got, "- UNUSABLE NOW")
	if unusableLine == "" {
		t.Fatal("提示词未标注「当前不可用」字段段 —— AI 会对着空数据产垃圾")
	}
	marked := fieldSet(unusableLine)
	for _, f := range []string{"pe", "pb", "ps", "roe", "roa", "revenue", "profit"} {
		if !marked[f] {
			t.Errorf("不可用字段 %q 未被点名禁用：%s", f, unusableLine)
		}
	}
}

// lineStartingWith 取出以 prefix 开头的那一行（用于按行断言，避免
// strings.Contains 被「另一段里的同名字段」误命中）。
func lineStartingWith(s, prefix string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.HasPrefix(ln, prefix) {
			return ln
		}
	}
	return ""
}

// fieldSet 把 "- Data fields: a, b, c" 解析成字段名集合（精确匹配）。
func fieldSet(line string) map[string]bool {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return nil
	}
	out := make(map[string]bool)
	for _, f := range strings.Split(line[idx+1:], ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			out[f] = true
		}
	}
	return out
}
