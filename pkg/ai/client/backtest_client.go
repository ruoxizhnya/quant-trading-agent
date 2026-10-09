package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ruoxizhnya/quant-trading/pkg/domain"
)

// BacktestClient is an HTTP client for the backtest API
type BacktestClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewBacktestClient creates a new backtest client
func NewBacktestClient(baseURL string) *BacktestClient {
	return &BacktestClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// BacktestRequest represents a backtest request
type BacktestRequest struct {
	StrategyName string   `json:"strategy_name"`
	StockPool    []string `json:"stock_pool"`
	StartDate    string   `json:"start_date"`
	EndDate      string   `json:"end_date"`
}

// BacktestResponse represents a backtest response
type BacktestResponse struct {
	Result *domain.BacktestResult `json:"result,omitempty"`
	Error  string                 `json:"error,omitempty"`
	JobID  string                 `json:"job_id,omitempty"`
	Status string                 `json:"status"`
}

// RunBacktest executes a backtest via the API
func (c *BacktestClient) RunBacktest(ctx context.Context, req BacktestRequest) (*domain.BacktestResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/backtest", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("backtest API returned status %d", resp.StatusCode)
	}

	var btResp BacktestResponse
	if err := json.NewDecoder(resp.Body).Decode(&btResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if btResp.Error != "" {
		return nil, fmt.Errorf("backtest failed: %s", btResp.Error)
	}

	return btResp.Result, nil
}

// GetBacktestResult polls for a backtest result by job ID
func (c *BacktestClient) GetBacktestResult(ctx context.Context, jobID string) (*domain.BacktestResult, error) {
	url := fmt.Sprintf("%s/api/backtest/%s", c.baseURL, jobID)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("backtest job not found: %s", jobID)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("backtest API returned status %d", resp.StatusCode)
	}

	var btResp BacktestResponse
	if err := json.NewDecoder(resp.Body).Decode(&btResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if btResp.Status == "pending" || btResp.Status == "running" {
		return nil, fmt.Errorf("backtest still running")
	}

	if btResp.Error != "" {
		return nil, fmt.Errorf("backtest failed: %s", btResp.Error)
	}

	return btResp.Result, nil
}

// Health checks if the backtest service is healthy
func (c *BacktestClient) Health(ctx context.Context) error {
	url := c.baseURL + "/health"

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}

	return nil
}
