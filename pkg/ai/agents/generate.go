package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
)

// GenerateAgent generates trading strategy code from natural language descriptions.
type GenerateAgent struct {
	llm *ai.Client

	// availability 是字段可用性地图（来自 expression.FieldAvailability 的
	// 真库探测）。nil = 尚未探测（提示词退回能力层全集）。
	// 与 ResearchAgent 同构：由调用方注入，本包不替调用方决定数据源。
	availability map[string]bool
}

// SetAvailability 注入字段可用性地图（OBS-08 切片 2）。
// 不注入则提示词不做可用性过滤（行为与切片 2 之前一致）。
func (a *GenerateAgent) SetAvailability(avail map[string]bool) {
	a.availability = avail
}

// NewGenerateAgent creates a new generate agent.
func NewGenerateAgent() *GenerateAgent {
	return &GenerateAgent{
		llm: ai.NewClient(),
	}
}

// StrategyTemplate represents a generated strategy template.
type StrategyTemplate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Code        string   `json:"code"`
	Params      []Param  `json:"params"`
	Factors     []string `json:"factors"`
	Confidence  float64  `json:"confidence"`
}

// Param represents a strategy parameter.
type Param struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Default     interface{} `json:"default"`
	Description string      `json:"description"`
}

// GenerateStrategy generates a strategy from a description.
func (a *GenerateAgent) GenerateStrategy(ctx context.Context, description string) (*StrategyTemplate, error) {
	if a.llm == nil || !a.llm.IsConfigured() {
		return nil, fmt.Errorf("AI client not configured")
	}

	prompt := fmt.Sprintf(`You are a quantitative strategy developer specializing in A-share market.

Strategy Description: "%s"

Generate a trading strategy with the following format:
1. Strategy Name (short, descriptive)
2. Type (momentum, mean_reversion, multi_factor, or custom)
3. Parameters (name, type, default, description)
4. Factor Formulas (list of factor expressions)
5. Strategy Logic (pseudocode for signal generation)

Factor Expression DSL Syntax:
%s

A-share market constraints (must be respected):
%s

Output ONLY valid JSON:
{
  "name": "strategy_name",
  "type": "momentum",
  "params": [
    {"name": "lookback", "type": "int", "default": 20, "description": "Lookback period"}
  ],
  "factors": ["ts_mean(close, 20)", "ts_std(close, 60)"],
  "logic": "Buy when price > moving average"
}`, description, factorDSLSyntax(a.availability), strategyDomainGuidance())

	messages := []ai.ChatMessage{
		{Role: "system", Content: "You are a quantitative strategy developer. Output ONLY valid JSON."},
		{Role: "user", Content: prompt},
	}

	resp, err := a.llm.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}

	resp = cleanJSONResponse(resp)

	// S7-P0-4 (ODR-043-4): parse with json.Unmarshal instead of the
	// former extractField string-scanner, which truncated values at
	// commas, escaped quotes, and newlines.
	parsed, err := parseStrategyTemplateJSON(resp)
	if err != nil || parsed.Name == "" {
		template := a.generateFallbackStrategy(description)
		return template, nil
	}

	template := &StrategyTemplate{
		ID:          generateStrategyID(),
		Name:        parsed.Name,
		Type:        parsed.Type,
		Description: description,
		Code:        parsed.Code,
		Params:      parsed.Params,
		Factors:     parsed.Factors,
		Confidence:  0.7,
	}

	return template, nil
}

// strategyDomainGuidance 是策略生成提示词里的**领域知识**段：A 股交易约束
// 与基本面字段的 PIT / NaN 语义。
//
// ─── 来源与为什么保留（OBS-08 切片 2 的 prompts 裁决）──────────────
// 这段内容原在 `pkg/ai/prompts/strategy_generate.txt`（零引用的死文件）。
// 清理时判定它**不是可重建的资产**：里面的洞察（负值 P/E 是亏损不是便宜、
// 故排名要用 neg(pe)）是自写的领域知识，无法从注册表或代码自动生成；而
// generate.go 原来的活提示词恰恰缺这些。故知识**合并进活提示词**，文件删除
// —— 消除「两份提示词」的双真相（与 OBS-06 / OBS-08 同一病根）。
//
// 抽成函数是为了可测：这段知识若被后人改丢，测试会红（见
// generate_prompt_test.go）。
func strategyDomainGuidance() string {
	return `- T+1: shares bought today cannot be sold today
- Daily price limit: ±10% (ST stocks ±5%)
- Short selling is not available for most stocks
- Include stop-loss / position sizing / max-drawdown control in the strategy logic

Fundamental fields (pe/pb/ps/roe/roa), when available:
- PIT: each bar only sees what was already disclosed that day
- They require the backtest engine to be given financial reports; without them
  the expression fails loudly (it does NOT silently substitute zeros)
- pe / pb / ps are NaN when non-positive — a negative P/E means the company is
  losing money, NOT that it is cheap. Ranking on neg(pe) therefore never
  rewards loss-makers.`
}

// GenerateFromFactors generates a strategy that combines multiple factors.
func (a *GenerateAgent) GenerateFromFactors(ctx context.Context, factorFormulas []string, description string) (*StrategyTemplate, error) {
	if len(factorFormulas) == 0 {
		return nil, fmt.Errorf("no factors provided")
	}

	factorsStr := strings.Join(factorFormulas, "\n")
	prompt := fmt.Sprintf(`Create a multi-factor strategy using these factors:
%s

Requirements: %s

Generate:
1. Strategy name
2. Signal logic (how to combine factors)
3. Position sizing rules
4. Risk management rules

Output JSON with fields: name, type, logic, params, factors`, factorsStr, description)

	messages := []ai.ChatMessage{
		{Role: "system", Content: "You are a quantitative strategy developer."},
		{Role: "user", Content: prompt},
	}

	resp, err := a.llm.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}

	resp = cleanJSONResponse(resp)

	// S7-P0-4 (ODR-043-4): json.Unmarshal instead of extractField.
	parsed, err := parseStrategyTemplateJSON(resp)
	if err != nil || parsed.Name == "" {
		parsed.Name = fmt.Sprintf("MultiFactor_%d", strategyIDCounter)
		parsed.Code = "Equal-weighted factor combination"
	}

	template := &StrategyTemplate{
		ID:          generateStrategyID(),
		Name:        parsed.Name,
		Type:        "multi_factor",
		Description: description,
		Code:        parsed.Code,
		Factors:     factorFormulas,
		Confidence:  0.75,
	}

	return template, nil
}

// strategyTemplateJSON is the JSON shape the LLM is prompted to emit
// for strategy generation. "logic" maps to the Code field (the prompt
// asks for "logic" as the pseudocode field name).
//
// S7-P0-4 (ODR-043-4): replaces extractField, which could not parse
// arrays (factors/params) or values containing commas / escaped quotes.
type strategyTemplateJSON struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Logic   string   `json:"logic"`
	Params  []Param  `json:"params"`
	Factors []string `json:"factors"`
}

// parseStrategyTemplateJSON parses an LLM response into a
// StrategyTemplate using json.Unmarshal. The response may be wrapped
// in ```json markdown fences (stripped first). Returns an error if the
// response is not valid JSON so the caller can fall back.
//
// S7-P0-4 (ODR-043-4): replaces extractField in generate.go.
func parseStrategyTemplateJSON(resp string) (StrategyTemplate, error) {
	cleaned := cleanJSONResponse(resp)
	if cleaned == "" {
		return StrategyTemplate{}, fmt.Errorf("empty response after cleaning")
	}

	var parsed strategyTemplateJSON
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return StrategyTemplate{}, fmt.Errorf("parse strategy template JSON: %w", err)
	}

	return StrategyTemplate{
		Name:    parsed.Name,
		Type:    parsed.Type,
		Code:    parsed.Logic,
		Params:  parsed.Params,
		Factors: parsed.Factors,
	}, nil
}

// generateFallbackStrategy creates a simple fallback strategy.
func (a *GenerateAgent) generateFallbackStrategy(description string) *StrategyTemplate {
	return &StrategyTemplate{
		ID:          generateStrategyID(),
		Name:        "SimpleMomentum",
		Type:        "momentum",
		Description: description,
		Code:        "Buy when close > moving average",
		Params: []Param{
			{Name: "lookback", Type: "int", Default: 20, Description: "Lookback period for MA"},
			{Name: "threshold", Type: "float", Default: 0.0, Description: "Signal threshold"},
		},
		Factors:    []string{"ts_mean(close, 20)", "close"},
		Confidence: 0.5,
	}
}

// Strategy generation helpers
var (
	strategyIDCounter int
	strategyIDMutex   sync.Mutex
)

func generateStrategyID() string {
	strategyIDMutex.Lock()
	defer strategyIDMutex.Unlock()
	strategyIDCounter++
	return fmt.Sprintf("strategy_%d", strategyIDCounter)
}
