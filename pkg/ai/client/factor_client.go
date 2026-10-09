package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// FactorClient is an HTTP client for factor computation API
type FactorClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewFactorClient creates a new factor client
func NewFactorClient(baseURL string) *FactorClient {
	return &FactorClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// ComputeFactorRequest represents a factor computation request
type ComputeFactorRequest struct {
	Formula   string   `json:"formula"`
	Symbols   []string `json:"symbols"`
	StartDate string   `json:"start_date"`
	EndDate   string   `json:"end_date"`
}

// ComputeFactorResponse represents a factor computation response
type ComputeFactorResponse struct {
	Values map[string][]float64 `json:"values,omitempty"`
	IC     float64              `json:"ic,omitempty"`
	IR     float64              `json:"ir,omitempty"`
	Error  string               `json:"error,omitempty"`
}

// ComputeFactor computes a factor expression for given symbols and date range
func (c *FactorClient) ComputeFactor(ctx context.Context, req ComputeFactorRequest) (map[string][]float64, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/factor/compute", bytes.NewReader(body))
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
		return nil, fmt.Errorf("factor API returned status %d", resp.StatusCode)
	}

	var factorResp ComputeFactorResponse
	if err := json.NewDecoder(resp.Body).Decode(&factorResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if factorResp.Error != "" {
		return nil, fmt.Errorf("factor computation failed: %s", factorResp.Error)
	}

	return factorResp.Values, nil
}

// EvaluateFactor evaluates a single factor expression and returns IC/IR metrics
func (c *FactorClient) EvaluateFactor(ctx context.Context, formula string, symbols []string, startDate, endDate string) (*FactorMetrics, error) {
	req := ComputeFactorRequest{
		Formula:   formula,
		Symbols:   symbols,
		StartDate: startDate,
		EndDate:   endDate,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/factor/evaluate", bytes.NewReader(body))
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
		return nil, fmt.Errorf("factor API returned status %d", resp.StatusCode)
	}

	var factorResp ComputeFactorResponse
	if err := json.NewDecoder(resp.Body).Decode(&factorResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if factorResp.Error != "" {
		return nil, fmt.Errorf("factor evaluation failed: %s", factorResp.Error)
	}

	return &FactorMetrics{
		IC: factorResp.IC,
		IR: factorResp.IR,
	}, nil
}

// FactorMetrics represents factor quality metrics
type FactorMetrics struct {
	IC float64 `json:"ic"`
	IR float64 `json:"ir"`
}

// Health checks if the factor service is healthy
func (c *FactorClient) Health(ctx context.Context) error {
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
