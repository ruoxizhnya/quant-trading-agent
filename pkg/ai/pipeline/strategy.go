package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/agents"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/client"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/gene_pool"
	"github.com/ruoxizhnya/quant-trading-agent/pkg/ai/validator"
)

// StrategyPipeline orchestrates strategy generation, validation, and backtesting.
type StrategyPipeline struct {
	generateAgent *agents.GenerateAgent
	validateAgent *agents.ValidateAgent
	codeValidator *validator.CodeValidator
	strategyPool  *gene_pool.StrategyPool
	btClient      *client.BacktestClient
}

// StrategyPipelineConfig configures the strategy pipeline.
type StrategyPipelineConfig struct {
	Description     string
	ValidationLevel agents.ValidationLevel
	RunBacktest     bool
	StockPool       []string
	StartDate       string
	EndDate         string
}

// StrategyPipelineResult holds the result of strategy pipeline execution.
type StrategyPipelineResult struct {
	Generated  int      `json:"generated"`
	Validated  int      `json:"validated"`
	Compiled   int      `json:"compiled"`
	Backtested int      `json:"backtested"`
	Errors     []string `json:"errors,omitempty"`
	StrategyID string   `json:"strategy_id,omitempty"`
}

// NewStrategyPipeline creates a new strategy pipeline.
func NewStrategyPipeline(
	generateAgent *agents.GenerateAgent,
	validateAgent *agents.ValidateAgent,
	strategyPool *gene_pool.StrategyPool,
	btClient *client.BacktestClient,
) *StrategyPipeline {
	return &StrategyPipeline{
		generateAgent: generateAgent,
		validateAgent: validateAgent,
		codeValidator: validator.NewCodeValidator(),
		strategyPool:  strategyPool,
		btClient:      btClient,
	}
}

// Run executes the full strategy pipeline: Generate → Validate → Compile → Backtest.
func (p *StrategyPipeline) Run(ctx context.Context, config StrategyPipelineConfig) (*StrategyPipelineResult, error) {
	result := &StrategyPipelineResult{
		Errors: []string{},
	}

	// Phase 1: Generate strategy template
	template, err := p.generateAgent.GenerateStrategy(ctx, config.Description)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("generation failed: %v", err))
		return result, err
	}
	result.Generated = 1

	// Phase 2: Validate strategy code syntax
	validation := p.codeValidator.ValidateSyntax(template.Code)
	if !validation.SyntaxValid {
		result.Errors = append(result.Errors, validation.Errors...)
		return result, fmt.Errorf("syntax validation failed")
	}
	result.Validated = 1

	// Phase 3: Compile strategy code
	compileResult := p.codeValidator.ValidateCompilation(template.Code)
	if !compileResult.Compiles {
		result.Errors = append(result.Errors, compileResult.Errors...)
		return result, fmt.Errorf("compilation failed")
	}
	result.Compiled = 1

	// Phase 4: Run backtest if configured
	if config.RunBacktest && p.btClient != nil {
		btReq := client.BacktestRequest{
			StrategyName: template.Name,
			StockPool:    config.StockPool,
			StartDate:    config.StartDate,
			EndDate:      config.EndDate,
		}

		btResult, err := p.btClient.RunBacktest(ctx, btReq)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("backtest failed: %v", err))
		} else {
			result.Backtested = 1
			// Save to strategy pool with backtest results
			if p.strategyPool != nil {
				gene := &gene_pool.StrategyGene{
					ID:           template.ID,
					Name:         template.Name,
					Description:  template.Description,
					StrategyType: template.Type,
					Code:         template.Code,
					TotalReturn:  btResult.TotalReturn,
					Sharpe:       btResult.SharpeRatio,
					Status:       "validated",
				}
				if err := p.strategyPool.Save(ctx, gene); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("save failed: %v", err))
				}
			}
		}
	}

	result.StrategyID = template.ID
	return result, nil
}

// GenerateFromFactors creates a strategy from existing factors.
func (p *StrategyPipeline) GenerateFromFactors(ctx context.Context, factorFormulas []string, description string) (*agents.StrategyTemplate, error) {
	return p.generateAgent.GenerateFromFactors(ctx, factorFormulas, description)
}

// RenderTemplate renders a strategy template with Go code template.
func (p *StrategyPipeline) RenderTemplate(templateStr string, data interface{}) (string, error) {
	tmpl, err := template.New("strategy").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return buf.String(), nil
}

// BatchRun runs multiple strategy generation tasks.
func (p *StrategyPipeline) BatchRun(ctx context.Context, configs []StrategyPipelineConfig) ([]*StrategyPipelineResult, error) {
	var results []*StrategyPipelineResult

	for _, config := range configs {
		result, err := p.Run(ctx, config)
		if err != nil {
			// Continue with other configs even if one fails
			if result == nil {
				result = &StrategyPipelineResult{
					Errors: []string{fmt.Sprintf("pipeline error: %v", err)},
				}
			}
		}
		results = append(results, result)
	}

	return results, nil
}
