package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
	"github.com/ruoxizhnya/quant-trading/pkg/expression"
)

// factorDSLSyntax 生成研究提示词里的「Factor Expression DSL Syntax」段。
//
// OBS-08：字段与算子的清单**从注册表派生**（pkg/expression 是唯一事实源），
// 不再硬编码。硬编码的代价是双向的 —— 2026-10-09 实测该段曾广告 `market_cap`
// （provider 永不可供的幻影字段，会让 AI 产出必然过不了闸门的表达式），
// 同时漏掉 `ps`/`roa`/`revenue`/`profit` 与 9 个算子
// （`ts_max`/`ts_min`/`ts_sum`/`ts_ewma`/`ts_rma`/`ts_kalman`/`cs_neutralize`/
// `neg`/`exp` 与全部比较算子），让 AI 白白少用已有能力。
// factorDSLSyntax 渲染提示词里的 DSL 语法段（算子与字段清单一律**从注册表
// 派生**，不硬编码）。
//
// avail 是字段可用性地图（来自 expression.FieldAvailability 的真库探测）：
//   - 非 nil 时：只把**有数据**的字段列进「可用」，并把「已登记但源表为空」
//     的字段单列一段、明确禁止使用 —— 这是 OBS-08 切片 2 的落点；
//   - nil 时：未做可用性探测，退回能力层全集（不列「不可用」段）。
//
// ─── 为什么要单列「不可用」而不是干脆不提 ──────────────────────────
// 实测 stock_fundamentals / stock_sector_map 均为 0 行，于是 pe/pb/ps/roe/
// roa/revenue/profit/sector 这 8 个字段语法合法、过闸门、provider 也认，
// **但求值必然拿不到数据**。只把它们从清单里删掉，AI 会因为「记得有这些
// 字段」而反复尝试；明确标注「已知但当前无数据、别用」，才能让它一次就绕开。
func factorDSLSyntax(avail map[string]bool) string {
	var tsOps, csOps, mathOps, binOps []string
	for _, op := range expression.AvailableOperators() { // 已按字典序
		switch {
		case expression.IsTimeSeriesOp(op):
			tsOps = append(tsOps, op)
		case expression.IsCrossSectionalOp(op):
			csOps = append(csOps, op)
		case expression.IsMathOp(op):
			mathOps = append(mathOps, op)
		default:
			binOps = append(binOps, op)
		}
	}

	fields := expression.AvailableDataFieldsWith(avail)
	out := "- Data fields: " + strings.Join(fields, ", ") + "\n"
	if avail != nil {
		if unusable := unavailableDataFields(avail); len(unusable) > 0 {
			out += "- UNUSABLE NOW (registered, but their source table is EMPTY — " +
				"do NOT use them; any formula using them yields no data): " +
				strings.Join(unusable, ", ") + "\n"
		}
	}
	out += "- Time-series ops: " + strings.Join(tsOps, ", ") + "\n" +
		"- Cross-sectional ops: " + strings.Join(csOps, ", ") + "\n" +
		"- Math ops: " + strings.Join(mathOps, ", ") + "\n" +
		"- Arithmetic / comparison: " + strings.Join(binOps, ", ")
	return out
}

// unavailableDataFields 返回「已登记但当前无数据」的数据字段（升序）。
// avail 为 nil 时返回空（未探测 = 不做不可用判断）。
func unavailableDataFields(avail map[string]bool) []string {
	if avail == nil {
		return nil
	}
	out := make([]string, 0)
	for _, f := range expression.AvailableDataFields() {
		if !avail[f] {
			out = append(out, f)
		}
	}
	return out
}

// ResearchAgent generates factor hypotheses and validates them
type ResearchAgent struct {
	llm *ai.Client

	// availability 是字段可用性地图（来自 expression.FieldAvailability 的
	// 真库探测）。nil = 尚未探测（提示词退回能力层全集）。
	//
	// 之所以做成**可注入**而非构造时直连 DB：本包（AI 侧）不该替调用方决定
	// 数据源，且 ResearchAgent 当前**零生产构造点**（只有测试在用），留注入
	// 口比在构造函数里塞一个 pool 更轻。
	availability map[string]bool
}

// NewResearchAgent creates a new research agent
func NewResearchAgent() *ResearchAgent {
	return &ResearchAgent{
		llm: ai.NewClient(),
	}
}

// SetAvailability 注入字段可用性地图（OBS-08 切片 2）。
//
// 调用方应在生成假设前用 expression.FieldAvailability(ctx, probe) 探测一次
// 并注入；不注入则提示词不做可用性过滤（行为与切片 2 之前一致）。
func (a *ResearchAgent) SetAvailability(avail map[string]bool) {
	a.availability = avail
}

// FactorHypothesis represents a generated factor hypothesis
type FactorHypothesis struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Formula     string  `json:"formula"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
	Rationale   string  `json:"rationale"`
}

// GenerateHypothesis generates a factor hypothesis from a research topic
func (a *ResearchAgent) GenerateHypothesis(ctx context.Context, topic string) (*FactorHypothesis, error) {
	if a.llm == nil || !a.llm.IsConfigured() {
		return nil, fmt.Errorf("AI client not configured")
	}

	// Quality 示例依赖基本面字段；源表为空时它反而是**误导**（提示词自己
	// 示范了一个必然拿不到数据的写法），故不可用则换成明确的跳过说明。
	qualityExample := "- Quality: roe / pe"
	if a.availability != nil && (!a.availability["roe"] || !a.availability["pe"]) {
		qualityExample = "- Quality: (SKIP — fundamental fields have no data right now; prefer price/volume factors)"
	}

	prompt := fmt.Sprintf(`You are a quantitative research analyst specializing in A-share market factors.

Research Topic: "%s"

Generate a factor hypothesis with the following format:
1. Factor Name (short, descriptive)
2. Category (momentum, value, quality, volatility, liquidity, or custom)
3. Formula (using the factor expression DSL)
4. Rationale (why this factor should work in A-share market)

Factor Expression DSL Syntax:
%s

Example formulas:
- Momentum: ts_pct_change(close, 20)
- Mean Reversion: cs_rank(ts_mean(close, 5) / ts_mean(close, 20))
- Volatility: ts_std(close, 20) / ts_mean(close, 20)
%s

Output ONLY valid JSON:
{
  "name": "factor_name",
  "category": "momentum",
  "formula": "ts_pct_change(close, 20)",
  "rationale": "explanation"
}`, topic, factorDSLSyntax(a.availability), qualityExample)

	messages := []ai.ChatMessage{
		{Role: "system", Content: "You are a quantitative research analyst. Output ONLY valid JSON."},
		{Role: "user", Content: prompt},
	}

	resp, err := a.llm.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}

	// Clean and parse response
	resp = cleanJSONResponse(resp)

	hypothesis, err := parseFactorHypothesisJSON(resp)
	if err != nil || hypothesis.Formula == "" {
		// Fallback: generate a simple formula based on topic.
		// S7-P0-4 (ODR-043-4): parseFactorHypothesisJSON now returns
		// an error on invalid JSON (replacing the old extractField
		// which silently returned "" on parse failure). Either way
		// we fall back to a deterministic hypothesis.
		hypothesis = a.generateFallbackHypothesis(topic)
	} else {
		hypothesis.ID = generateID()
		hypothesis.Description = topic
		hypothesis.Confidence = 0.7
	}

	return &hypothesis, nil
}

// factorHypothesisJSON is the JSON shape the LLM is prompted to emit.
// It maps the prompt field names to the FactorHypothesis struct fields.
// "rationale" in the JSON maps to Rationale; "formula" to Formula; etc.
//
// S7-P0-4 (ODR-043-4): previously parsed by extractField (a naive
// string scanner that truncated values at the first comma, escaped
// quote, or newline). json.Unmarshal handles all JSON edge cases
// correctly.
type factorHypothesisJSON struct {
	Name      string `json:"name"`
	Category  string `json:"category"`
	Formula   string `json:"formula"`
	Rationale string `json:"rationale"`
}

// parseFactorHypothesisJSON parses an LLM response into a
// FactorHypothesis using json.Unmarshal. The response may be wrapped
// in ```json markdown fences (stripped first). Returns an error if the
// response is not valid JSON so the caller can fall back.
//
// S7-P0-4 (ODR-043-4): replaces extractField, which could not handle
// commas in formulas, escaped quotes in rationales, or newlines in
// multi-line text.
func parseFactorHypothesisJSON(resp string) (FactorHypothesis, error) {
	cleaned := cleanJSONResponse(resp)
	if cleaned == "" {
		return FactorHypothesis{}, fmt.Errorf("empty response after cleaning")
	}

	var parsed factorHypothesisJSON
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return FactorHypothesis{}, fmt.Errorf("parse factor hypothesis JSON: %w", err)
	}

	return FactorHypothesis{
		Name:      parsed.Name,
		Category:  parsed.Category,
		Formula:   parsed.Formula,
		Rationale: parsed.Rationale,
	}, nil
}

// ValidateFormula validates a factor formula by parsing it
func (a *ResearchAgent) ValidateFormula(formula string) (*expression.Expression, error) {
	parser := expression.NewParser()
	expr, err := parser.Parse(formula)
	if err != nil {
		return nil, fmt.Errorf("formula validation failed: %w", err)
	}

	if err := expr.Validate(); err != nil {
		return nil, fmt.Errorf("expression validation failed: %w", err)
	}

	return expr, nil
}

// generateFallbackHypothesis creates a simple hypothesis when LLM fails
func (a *ResearchAgent) generateFallbackHypothesis(topic string) FactorHypothesis {
	lowerTopic := strings.ToLower(topic)

	switch {
	case strings.Contains(lowerTopic, "momentum") || strings.Contains(lowerTopic, "动量"):
		return FactorHypothesis{
			ID:          generateID(),
			Name:        "momentum_20d",
			Category:    "momentum",
			Formula:     "ts_pct_change(close, 20)",
			Rationale:   "20-day price momentum captures short-term trend persistence",
			Confidence:  0.6,
			Description: topic,
		}
	case strings.Contains(lowerTopic, "value") || strings.Contains(lowerTopic, "价值"):
		return FactorHypothesis{
			ID:          generateID(),
			Name:        "pe_ratio",
			Category:    "value",
			Formula:     "1 / pe",
			Rationale:   "Low PE ratio indicates potential undervaluation",
			Confidence:  0.6,
			Description: topic,
		}
	case strings.Contains(lowerTopic, "quality") || strings.Contains(lowerTopic, "质量"):
		return FactorHypothesis{
			ID:          generateID(),
			Name:        "roe_quality",
			Category:    "quality",
			Formula:     "roe",
			Rationale:   "High ROE indicates strong profitability",
			Confidence:  0.6,
			Description: topic,
		}
	case strings.Contains(lowerTopic, "volatility") || strings.Contains(lowerTopic, "波动"):
		return FactorHypothesis{
			ID:          generateID(),
			Name:        "volatility_20d",
			Category:    "volatility",
			Formula:     "ts_std(close, 20) / ts_mean(close, 20)",
			Rationale:   "Volatility normalization captures relative risk",
			Confidence:  0.6,
			Description: topic,
		}
	default:
		return FactorHypothesis{
			ID:          generateID(),
			Name:        "custom_factor",
			Category:    "custom",
			Formula:     "close / ts_mean(close, 20)",
			Rationale:   "Price relative to moving average",
			Confidence:  0.5,
			Description: topic,
		}
	}
}

// Helper functions

var (
	idCounter int
	idMutex   sync.Mutex
)

func generateID() string {
	// Simple ID generation - in production use UUID
	idMutex.Lock()
	defer idMutex.Unlock()
	idCounter++
	return fmt.Sprintf("factor_%d", idCounter)
}

// extractField was removed in S7-P0-4 (ODR-043-4). It was a naive
// string scanner that truncated JSON values at the first comma,
// escaped quote, or newline — causing formulas like
// "rank(close), ts_mean(returns, 5)" to be silently cut to
// "rank(close)". Replaced by parseFactorHypothesisJSON (research.go)
// and parseStrategyTemplateJSON (generate.go), both of which use
// json.Unmarshal for correct, spec-compliant parsing.

func cleanJSONResponse(resp string) string {
	resp = strings.TrimSpace(resp)
	resp = strings.TrimPrefix(resp, "```json")
	resp = strings.TrimPrefix(resp, "```")
	resp = strings.TrimSuffix(resp, "```")
	return strings.TrimSpace(resp)
}
