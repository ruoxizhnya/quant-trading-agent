package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai"
)

// StrategyType represents the classification of strategy intent
type StrategyType string

const (
	StrategyTypeMomentum       StrategyType = "momentum"
	StrategyTypeMeanReversion  StrategyType = "mean_reversion"
	StrategyTypeTrendFollowing StrategyType = "trend_following"
	StrategyTypeBreakout       StrategyType = "breakout"
	StrategyTypeMultiFactor    StrategyType = "multi_factor"
	StrategyTypeValue          StrategyType = "value"
	StrategyTypeQuality        StrategyType = "quality"
	StrategyTypeCustom         StrategyType = "custom"
)

// Parameter represents an extracted strategy parameter
type Parameter struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"` // "int", "float", "string", "bool"
	Value       interface{} `json:"value"`
	Description string      `json:"description"`
	Min         *float64    `json:"min,omitempty"`
	Max         *float64    `json:"max,omitempty"`
}

// Intent represents the parsed user intent from natural language
type Intent struct {
	RawText         string           `json:"raw_text"`
	StrategyType    StrategyType     `json:"strategy_type"`
	StrategyName    string           `json:"strategy_name"`
	Description     string           `json:"description"`
	Parameters      []Parameter      `json:"parameters"`
	Indicators      []string         `json:"indicators"`
	Timeframe       string           `json:"timeframe"`
	Universe        string           `json:"universe"`
	RiskConstraints *RiskConstraints `json:"risk_constraints,omitempty"`
	Confidence      float64          `json:"confidence"`
}

// RiskConstraints represents risk management parameters
type RiskConstraints struct {
	MaxPositions   *int     `json:"max_positions,omitempty"`
	MaxDrawdown    *float64 `json:"max_drawdown,omitempty"`
	StopLoss       *float64 `json:"stop_loss,omitempty"`
	TakeProfit     *float64 `json:"take_profit,omitempty"`
	MaxTurnover    *float64 `json:"max_turnover,omitempty"`
	PositionSizing *string  `json:"position_sizing,omitempty"`
}

// Parser parses natural language strategy descriptions into structured intents
type Parser struct {
	llm *ai.Client
}

// NewParser creates a new intent parser
func NewParser() *Parser {
	return &Parser{
		llm: ai.NewClient(),
	}
}

// NewParserWithClient creates a new intent parser with a specific AI client
func NewParserWithClient(client *ai.Client) *Parser {
	return &Parser{llm: client}
}

// IsConfigured returns true if the LLM client is configured
func (p *Parser) IsConfigured() bool {
	return p.llm != nil && p.llm.IsConfigured()
}

// Parse converts natural language description into structured intent
func (p *Parser) Parse(ctx context.Context, description string) (*Intent, error) {
	if description == "" {
		return nil, fmt.Errorf("description cannot be empty")
	}

	// First, try rule-based extraction for common patterns
	intent := p.ruleBasedExtract(description)

	// If LLM is configured, use it to enhance extraction
	if p.IsConfigured() {
		enhanced, err := p.llmEnhance(ctx, description, intent)
		if err == nil {
			intent = enhanced
		}
	}

	// Set defaults and validate
	intent.setDefaults()
	if err := intent.validate(); err != nil {
		return nil, err
	}

	return intent, nil
}

// ruleBasedExtract performs pattern matching for common strategy descriptions
func (p *Parser) ruleBasedExtract(description string) *Intent {
	intent := &Intent{
		RawText:    description,
		Parameters: []Parameter{},
		Indicators: []string{},
		Confidence: 0.5,
		Universe:   "csi300",
		Timeframe:  "1d",
	}

	lower := strings.ToLower(description)

	// Strategy type classification
	switch {
	case containsAny(lower, []string{"momentum", "动量", "趋势跟踪", "追涨"}):
		intent.StrategyType = StrategyTypeMomentum
		intent.StrategyName = "momentum_strategy"
		intent.Description = "动量策略：买入上涨趋势中的股票"

	case containsAny(lower, []string{"mean reversion", "均值回归", "反转", "超跌"}):
		intent.StrategyType = StrategyTypeMeanReversion
		intent.StrategyName = "mean_reversion_strategy"
		intent.Description = "均值回归策略：买入超跌股票，卖出超买股票"

	case containsAny(lower, []string{"breakout", "突破", "通道突破"}):
		intent.StrategyType = StrategyTypeBreakout
		intent.StrategyName = "breakout_strategy"
		intent.Description = "突破策略：买入突破阻力位的股票"

	case containsAny(lower, []string{"multi factor", "多因子", "复合策略"}):
		intent.StrategyType = StrategyTypeMultiFactor
		intent.StrategyName = "multi_factor_strategy"
		intent.Description = "多因子策略：综合多个因子选股"

	case containsAny(lower, []string{"value", "价值", "低估", "pe", "pb"}):
		intent.StrategyType = StrategyTypeValue
		intent.StrategyName = "value_strategy"
		intent.Description = "价值策略：买入低估值股票"

	case containsAny(lower, []string{"quality", "质量", "roe", "盈利质量"}):
		intent.StrategyType = StrategyTypeQuality
		intent.StrategyName = "quality_strategy"
		intent.Description = "质量策略：买入高盈利质量股票"

	default:
		intent.StrategyType = StrategyTypeCustom
		intent.StrategyName = "custom_strategy"
		intent.Description = description
	}

	// Extract indicators
	intent.Indicators = extractIndicators(lower)

	// Extract numeric parameters
	intent.Parameters = extractParameters(lower, intent.StrategyType)

	// Extract universe
	intent.Universe = extractUniverse(lower)

	// Extract risk constraints
	intent.RiskConstraints = extractRiskConstraints(lower)

	// Extract timeframe
	intent.Timeframe = extractTimeframe(lower)

	return intent
}

// llmEnhance uses LLM to improve intent extraction
func (p *Parser) llmEnhance(ctx context.Context, description string, baseIntent *Intent) (*Intent, error) {
	prompt := fmt.Sprintf(`Parse the following Chinese/English trading strategy description into structured JSON.

Description: "%s"

Extract and return ONLY a JSON object with these fields:
{
  "strategy_type": "one of: momentum, mean_reversion, trend_following, breakout, multi_factor, value, quality, custom",
  "strategy_name": "snake_case strategy name",
  "description": "brief Chinese description",
  "indicators": ["list of technical indicators mentioned"],
  "timeframe": "e.g. 1d, 5d, 20d",
  "universe": "csi300, csi500, csi800, or all",
  "parameters": [
    {"name": "param_name", "type": "int|float|string|bool", "value": parsed_value, "description": "Chinese description"}
  ],
  "risk_constraints": {
    "max_positions": integer or null,
    "max_drawdown": float or null,
    "stop_loss": float or null,
    "take_profit": float or null,
    "position_sizing": "equal|volatility|signal_strength or null"
  }
}

Rules:
- If a parameter is not mentioned, omit it or set to null
- Use Chinese for description fields
- strategy_name must be snake_case
- Return ONLY valid JSON, no markdown, no explanation`, description)

	messages := []ai.ChatMessage{
		{Role: "system", Content: "You are a quantitative trading strategy parser. You extract structured parameters from natural language descriptions. Output ONLY valid JSON."},
		{Role: "user", Content: prompt},
	}

	resp, err := p.llm.Chat(ctx, messages)
	if err != nil {
		return baseIntent, err
	}

	// Clean up response - remove markdown fences
	resp = cleanJSONResponse(resp)

	var llmResult struct {
		StrategyType    string           `json:"strategy_type"`
		StrategyName    string           `json:"strategy_name"`
		Description     string           `json:"description"`
		Indicators      []string         `json:"indicators"`
		Timeframe       string           `json:"timeframe"`
		Universe        string           `json:"universe"`
		Parameters      []Parameter      `json:"parameters"`
		RiskConstraints *RiskConstraints `json:"risk_constraints"`
	}

	if err := json.Unmarshal([]byte(resp), &llmResult); err != nil {
		return baseIntent, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// Merge LLM results with base intent
	if llmResult.StrategyType != "" {
		baseIntent.StrategyType = StrategyType(llmResult.StrategyType)
	}
	if llmResult.StrategyName != "" {
		baseIntent.StrategyName = llmResult.StrategyName
	}
	if llmResult.Description != "" {
		baseIntent.Description = llmResult.Description
	}
	if len(llmResult.Indicators) > 0 {
		baseIntent.Indicators = llmResult.Indicators
	}
	if llmResult.Timeframe != "" {
		baseIntent.Timeframe = llmResult.Timeframe
	}
	if llmResult.Universe != "" {
		baseIntent.Universe = llmResult.Universe
	}
	if len(llmResult.Parameters) > 0 {
		baseIntent.Parameters = mergeParameters(baseIntent.Parameters, llmResult.Parameters)
	}
	if llmResult.RiskConstraints != nil {
		baseIntent.RiskConstraints = llmResult.RiskConstraints
	}

	baseIntent.Confidence = 0.85
	return baseIntent, nil
}

// setDefaults fills in default values for missing fields
func (i *Intent) setDefaults() {
	if i.StrategyName == "" {
		i.StrategyName = "custom_strategy"
	}
	if i.Universe == "" {
		i.Universe = "csi300"
	}
	if i.Timeframe == "" {
		i.Timeframe = "1d"
	}
	if i.Description == "" {
		i.Description = i.RawText
	}

	// Add default parameters based on strategy type if none extracted
	if len(i.Parameters) == 0 {
		i.Parameters = getDefaultParameters(i.StrategyType)
	}

	// Ensure risk constraints exist
	if i.RiskConstraints == nil {
		i.RiskConstraints = &RiskConstraints{}
	}
}

// validate checks if the intent is valid
func (i *Intent) validate() error {
	if i.RawText == "" {
		return fmt.Errorf("raw text is required")
	}
	if i.StrategyType == "" {
		return fmt.Errorf("strategy type is required")
	}
	return nil
}

// ToYAML converts the intent to YAML configuration format
func (i *Intent) ToYAML() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("strategy:\n"))
	b.WriteString(fmt.Sprintf("  name: %s\n", i.StrategyName))
	b.WriteString(fmt.Sprintf("  type: %s\n", i.StrategyType))
	b.WriteString(fmt.Sprintf("  description: %s\n", i.Description))
	b.WriteString(fmt.Sprintf("  universe: %s\n", i.Universe))
	b.WriteString(fmt.Sprintf("  timeframe: %s\n", i.Timeframe))

	if len(i.Indicators) > 0 {
		b.WriteString(fmt.Sprintf("  indicators:\n"))
		for _, ind := range i.Indicators {
			b.WriteString(fmt.Sprintf("    - %s\n", ind))
		}
	}

	if len(i.Parameters) > 0 {
		b.WriteString(fmt.Sprintf("  parameters:\n"))
		for _, p := range i.Parameters {
			b.WriteString(fmt.Sprintf("    %s:\n", p.Name))
			b.WriteString(fmt.Sprintf("      type: %s\n", p.Type))
			b.WriteString(fmt.Sprintf("      value: %v\n", p.Value))
			if p.Description != "" {
				b.WriteString(fmt.Sprintf("      description: %s\n", p.Description))
			}
			if p.Min != nil {
				b.WriteString(fmt.Sprintf("      min: %v\n", *p.Min))
			}
			if p.Max != nil {
				b.WriteString(fmt.Sprintf("      max: %v\n", *p.Max))
			}
		}
	}

	if i.RiskConstraints != nil {
		b.WriteString(fmt.Sprintf("  risk:\n"))
		if i.RiskConstraints.MaxPositions != nil {
			b.WriteString(fmt.Sprintf("    max_positions: %d\n", *i.RiskConstraints.MaxPositions))
		}
		if i.RiskConstraints.MaxDrawdown != nil {
			b.WriteString(fmt.Sprintf("    max_drawdown: %.2f\n", *i.RiskConstraints.MaxDrawdown))
		}
		if i.RiskConstraints.StopLoss != nil {
			b.WriteString(fmt.Sprintf("    stop_loss: %.2f\n", *i.RiskConstraints.StopLoss))
		}
		if i.RiskConstraints.TakeProfit != nil {
			b.WriteString(fmt.Sprintf("    take_profit: %.2f\n", *i.RiskConstraints.TakeProfit))
		}
		if i.RiskConstraints.PositionSizing != nil {
			b.WriteString(fmt.Sprintf("    position_sizing: %s\n", *i.RiskConstraints.PositionSizing))
		}
	}

	return b.String()
}

// Helper functions

func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func extractIndicators(text string) []string {
	lower := strings.ToLower(text)
	indicatorPatterns := map[string][]string{
		"rsi":       {"rsi", "相对强弱指标"},
		"macd":      {"macd", "异同移动平均线"},
		"ma":        {"ma", "均线", "移动平均线", "sma", "ema"},
		"bollinger": {"bollinger", "布林带", "boll"},
		"kdj":       {"kdj", "随机指标"},
		"atr":       {"atr", "平均真实波幅"},
		"obv":       {"obv", "能量潮"},
		"volume":    {"volume", "成交量", "vol", "量能"},
		"pe":        {"pe", "市盈率"},
		"pb":        {"pb", "市净率"},
		"roe":       {"roe", "净资产收益率"},
	}

	var indicators []string
	for ind, patterns := range indicatorPatterns {
		if containsAny(lower, patterns) {
			indicators = append(indicators, ind)
		}
	}
	return indicators
}

func extractParameters(text string, strategyType StrategyType) []Parameter {
	var params []Parameter
	lower := strings.ToLower(text)

	// Extract lookback period
	if matches := regexp.MustCompile(`(\d+)\s*日`).FindStringSubmatch(text); len(matches) > 1 {
		if val, err := strconv.Atoi(matches[1]); err == nil {
			params = append(params, Parameter{
				Name:        "lookback_days",
				Type:        "int",
				Value:       val,
				Description: "回看天数",
				Min:         floatPtr(5),
				Max:         floatPtr(250),
			})
		}
	}

	// Extract RSI threshold
	if strings.Contains(lower, "rsi") || strings.Contains(text, "相对强弱") {
		overbought := 70.0
		oversold := 30.0

		if matches := regexp.MustCompile(`(超买|overbought).*?(\d+)`).FindStringSubmatch(text); len(matches) > 2 {
			if val, err := strconv.ParseFloat(matches[2], 64); err == nil {
				overbought = val
			}
		}
		if matches := regexp.MustCompile(`(超卖|oversold).*?(\d+)`).FindStringSubmatch(text); len(matches) > 2 {
			if val, err := strconv.ParseFloat(matches[2], 64); err == nil {
				oversold = val
			}
		}

		params = append(params,
			Parameter{Name: "rsi_overbought", Type: "float", Value: overbought, Description: "RSI超买阈值", Min: floatPtr(50), Max: floatPtr(100)},
			Parameter{Name: "rsi_oversold", Type: "float", Value: oversold, Description: "RSI超卖阈值", Min: floatPtr(0), Max: floatPtr(50)},
		)
	}

	// Extract MA periods
	if strings.Contains(text, "均线") || strings.Contains(lower, "ma") {
		shortMA := 5
		longMA := 20

		if matches := regexp.MustCompile(`(短期|short).*?(\d+)`).FindStringSubmatch(text); len(matches) > 2 {
			if val, err := strconv.Atoi(matches[2]); err == nil {
				shortMA = val
			}
		}
		if matches := regexp.MustCompile(`(长期|long).*?(\d+)`).FindStringSubmatch(text); len(matches) > 2 {
			if val, err := strconv.Atoi(matches[2]); err == nil {
				longMA = val
			}
		}

		params = append(params,
			Parameter{Name: "short_ma", Type: "int", Value: shortMA, Description: "短期均线周期", Min: floatPtr(2), Max: floatPtr(60)},
			Parameter{Name: "long_ma", Type: "int", Value: longMA, Description: "长期均线周期", Min: floatPtr(5), Max: floatPtr(250)},
		)
	}

	// Extract position limit
	if matches := regexp.MustCompile(`(最多|最大).*?(\d+)\s*(只|支|个)`).FindStringSubmatch(text); len(matches) > 2 {
		if val, err := strconv.Atoi(matches[2]); err == nil {
			params = append(params, Parameter{
				Name:        "max_positions",
				Type:        "int",
				Value:       val,
				Description: "最大持仓数量",
				Min:         floatPtr(1),
				Max:         floatPtr(100),
			})
		}
	}

	return params
}

func extractUniverse(text string) string {
	switch {
	case strings.Contains(text, "沪深300") || strings.Contains(text, "csi300"):
		return "csi300"
	case strings.Contains(text, "中证500") || strings.Contains(text, "csi500"):
		return "csi500"
	case strings.Contains(text, "中证800") || strings.Contains(text, "csi800"):
		return "csi800"
	case strings.Contains(text, "全市场") || strings.Contains(text, "全部") || strings.Contains(text, "all"):
		return "all"
	default:
		return "csi300"
	}
}

func extractRiskConstraints(text string) *RiskConstraints {
	rc := &RiskConstraints{}

	// Extract stop loss
	if matches := regexp.MustCompile(`(止损|stop loss).*?(\d+(?:\.\d+)?)\s*%?`).FindStringSubmatch(text); len(matches) > 2 {
		if val, err := strconv.ParseFloat(matches[2], 64); err == nil {
			if val > 1 {
				val = val / 100 // Convert percentage to decimal
			}
			rc.StopLoss = &val
		}
	}

	// Extract take profit
	if matches := regexp.MustCompile(`(止盈|take profit).*?(\d+(?:\.\d+)?)\s*%?`).FindStringSubmatch(text); len(matches) > 2 {
		if val, err := strconv.ParseFloat(matches[2], 64); err == nil {
			if val > 1 {
				val = val / 100
			}
			rc.TakeProfit = &val
		}
	}

	// Extract max drawdown
	if matches := regexp.MustCompile(`(最大回撤|max drawdown).*?(\d+(?:\.\d+)?)\s*%?`).FindStringSubmatch(text); len(matches) > 2 {
		if val, err := strconv.ParseFloat(matches[2], 64); err == nil {
			if val > 1 {
				val = val / 100
			}
			rc.MaxDrawdown = &val
		}
	}

	// Extract max positions
	if matches := regexp.MustCompile(`(最多|最大).*?(\d+)\s*(只|支|个|仓位)`).FindStringSubmatch(text); len(matches) > 2 {
		if val, err := strconv.Atoi(matches[2]); err == nil {
			rc.MaxPositions = &val
		}
	}

	return rc
}

func extractTimeframe(text string) string {
	if matches := regexp.MustCompile(`(\d+)\s*日`).FindStringSubmatch(text); len(matches) > 1 {
		return matches[1] + "d"
	}
	if matches := regexp.MustCompile(`(\d+)\s*周`).FindStringSubmatch(text); len(matches) > 1 {
		return matches[1] + "w"
	}
	if matches := regexp.MustCompile(`(\d+)\s*月`).FindStringSubmatch(text); len(matches) > 1 {
		return matches[1] + "M"
	}
	return "1d"
}

func getDefaultParameters(strategyType StrategyType) []Parameter {
	switch strategyType {
	case StrategyTypeMomentum:
		return []Parameter{
			{Name: "lookback_days", Type: "int", Value: 20, Description: "动量计算回看天数", Min: floatPtr(5), Max: floatPtr(60)},
			{Name: "top_n", Type: "int", Value: 10, Description: "选股数量", Min: floatPtr(1), Max: floatPtr(50)},
		}
	case StrategyTypeMeanReversion:
		return []Parameter{
			{Name: "rsi_period", Type: "int", Value: 14, Description: "RSI周期", Min: floatPtr(5), Max: floatPtr(30)},
			{Name: "rsi_oversold", Type: "float", Value: 30.0, Description: "RSI超卖阈值", Min: floatPtr(10), Max: floatPtr(50)},
			{Name: "rsi_overbought", Type: "float", Value: 70.0, Description: "RSI超买阈值", Min: floatPtr(50), Max: floatPtr(90)},
		}
	case StrategyTypeBreakout:
		return []Parameter{
			{Name: "lookback_days", Type: "int", Value: 20, Description: "突破回看天数", Min: floatPtr(5), Max: floatPtr(60)},
			{Name: "breakout_threshold", Type: "float", Value: 0.02, Description: "突破阈值", Min: floatPtr(0.001), Max: floatPtr(0.1)},
		}
	case StrategyTypeValue:
		return []Parameter{
			{Name: "pe_max", Type: "float", Value: 20.0, Description: "最大市盈率", Min: floatPtr(5), Max: floatPtr(100)},
			{Name: "pb_max", Type: "float", Value: 2.0, Description: "最大市净率", Min: floatPtr(0.5), Max: floatPtr(10)},
		}
	default:
		return []Parameter{
			{Name: "lookback_days", Type: "int", Value: 20, Description: "回看天数", Min: floatPtr(5), Max: floatPtr(250)},
		}
	}
}

func mergeParameters(base, llm []Parameter) []Parameter {
	seen := make(map[string]bool)
	var merged []Parameter

	for _, p := range llm {
		if !seen[p.Name] {
			seen[p.Name] = true
			merged = append(merged, p)
		}
	}

	for _, p := range base {
		if !seen[p.Name] {
			seen[p.Name] = true
			merged = append(merged, p)
		}
	}

	return merged
}

func cleanJSONResponse(resp string) string {
	resp = strings.TrimSpace(resp)
	resp = strings.TrimPrefix(resp, "```json")
	resp = strings.TrimPrefix(resp, "```")
	resp = strings.TrimSuffix(resp, "```")
	return strings.TrimSpace(resp)
}

func floatPtr(f float64) *float64 {
	return &f
}
