package gene_pool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StrategyGene represents a strategy in the gene pool.
type StrategyGene struct {
	ID           string                 `json:"id" db:"id"`
	Name         string                 `json:"name" db:"name"`
	Description  string                 `json:"description" db:"description"`
	StrategyType string                 `json:"strategy_type" db:"strategy_type"`
	Code         string                 `json:"code" db:"code"`
	Params       map[string]interface{} `json:"params" db:"params"`
	FactorIDs    []string               `json:"factor_ids" db:"factor_ids"`
	ParentIDs    []string               `json:"parent_ids" db:"parent_ids"`
	TotalReturn  float64                `json:"total_return" db:"total_return"`
	Sharpe       float64                `json:"sharpe" db:"sharpe"`
	MaxDrawdown  float64                `json:"max_drawdown" db:"max_drawdown"`
	WinRate      float64                `json:"win_rate" db:"win_rate"`
	Fitness      float64                `json:"fitness" db:"fitness"`
	Generation   int                    `json:"generation" db:"generation"`
	Status       string                 `json:"status" db:"status"`
	CreatedAt    time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at" db:"updated_at"`
}

// StrategyPool manages strategy genes in PostgreSQL.
type StrategyPool struct {
	pool *pgxpool.Pool
}

// NewStrategyPool creates a new StrategyPool.
func NewStrategyPool(pool *pgxpool.Pool) *StrategyPool {
	return &StrategyPool{pool: pool}
}

// Save persists a strategy gene to the database.
func (p *StrategyPool) Save(ctx context.Context, gene *StrategyGene) error {
	params, err := json.Marshal(gene.Params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	factorIDs, err := json.Marshal(gene.FactorIDs)
	if err != nil {
		return fmt.Errorf("marshal factor_ids: %w", err)
	}

	parentIDs, err := json.Marshal(gene.ParentIDs)
	if err != nil {
		return fmt.Errorf("marshal parent_ids: %w", err)
	}

	query := `
		INSERT INTO strategy_genes (
			id, name, description, strategy_type, code, params,
			factor_ids, parent_ids, total_return, sharpe, max_drawdown,
			win_rate, fitness, generation, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			strategy_type = EXCLUDED.strategy_type,
			code = EXCLUDED.code,
			params = EXCLUDED.params,
			factor_ids = EXCLUDED.factor_ids,
			parent_ids = EXCLUDED.parent_ids,
			total_return = EXCLUDED.total_return,
			sharpe = EXCLUDED.sharpe,
			max_drawdown = EXCLUDED.max_drawdown,
			win_rate = EXCLUDED.win_rate,
			fitness = EXCLUDED.fitness,
			generation = EXCLUDED.generation,
			status = EXCLUDED.status,
			updated_at = EXCLUDED.updated_at
	`

	_, err = p.pool.Exec(ctx, query,
		gene.ID, gene.Name, gene.Description, gene.StrategyType, gene.Code, params,
		factorIDs, parentIDs, gene.TotalReturn, gene.Sharpe, gene.MaxDrawdown,
		gene.WinRate, gene.Fitness, gene.Generation, gene.Status, gene.CreatedAt, gene.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("save strategy gene: %w", err)
	}
	return nil
}

// Get retrieves a strategy gene by ID.
func (p *StrategyPool) Get(ctx context.Context, id string) (*StrategyGene, error) {
	query := `
		SELECT id, name, description, strategy_type, code, params,
			factor_ids, parent_ids, total_return, sharpe, max_drawdown,
			win_rate, fitness, generation, status, created_at, updated_at
		FROM strategy_genes WHERE id = $1
	`

	row := p.pool.QueryRow(ctx, query, id)
	gene := &StrategyGene{}
	var params, factorIDs, parentIDs []byte

	err := row.Scan(
		&gene.ID, &gene.Name, &gene.Description, &gene.StrategyType, &gene.Code, &params,
		&factorIDs, &parentIDs, &gene.TotalReturn, &gene.Sharpe, &gene.MaxDrawdown,
		&gene.WinRate, &gene.Fitness, &gene.Generation, &gene.Status, &gene.CreatedAt, &gene.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get strategy gene: %w", err)
	}

	if err := unmarshalStrategyGeneFields(gene, params, factorIDs, parentIDs); err != nil {
		return nil, err
	}

	return gene, nil
}

// unmarshalStrategyGeneFields parses the JSON blob columns scanned from
// the strategy_genes table (params / factor_ids / parent_ids) into the
// gene struct. Empty blobs are skipped. Returns an error if any blob is
// non-empty but malformed JSON, so callers don't silently get a gene
// with zero-valued Params / FactorIDs / ParentIDs.
//
// S7-P0-6 (ODR-043): previously each unmarshal call discarded its
// error, masking data corruption (e.g. a truncated params blob would
// yield an empty Params map with no signal).
func unmarshalStrategyGeneFields(gene *StrategyGene, params, factorIDs, parentIDs []byte) error {
	if len(params) > 0 {
		if err := json.Unmarshal(params, &gene.Params); err != nil {
			return fmt.Errorf("parse strategy gene params: %w", err)
		}
	}
	if len(factorIDs) > 0 {
		if err := json.Unmarshal(factorIDs, &gene.FactorIDs); err != nil {
			return fmt.Errorf("parse strategy gene factor_ids: %w", err)
		}
	}
	if len(parentIDs) > 0 {
		if err := json.Unmarshal(parentIDs, &gene.ParentIDs); err != nil {
			return fmt.Errorf("parse strategy gene parent_ids: %w", err)
		}
	}
	return nil
}

// List retrieves strategy genes with optional filters.
func (p *StrategyPool) List(ctx context.Context, strategyType string, status string, minFitness float64, limit int) ([]*StrategyGene, error) {
	query := `
		SELECT id, name, description, strategy_type, code, params,
			factor_ids, parent_ids, total_return, sharpe, max_drawdown,
			win_rate, fitness, generation, status, created_at, updated_at
		FROM strategy_genes
		WHERE ($1 = '' OR strategy_type = $1)
			AND ($2 = '' OR status = $2)
			AND fitness >= $3
		ORDER BY fitness DESC, sharpe DESC
		LIMIT $4
	`

	rows, err := p.pool.Query(ctx, query, strategyType, status, minFitness, limit)
	if err != nil {
		return nil, fmt.Errorf("list strategy genes: %w", err)
	}
	defer rows.Close()

	return scanStrategyRows(rows)
}

// ListByGeneration retrieves strategy genes for a specific generation.
func (p *StrategyPool) ListByGeneration(ctx context.Context, generation int) ([]*StrategyGene, error) {
	query := `
		SELECT id, name, description, strategy_type, code, params,
			factor_ids, parent_ids, total_return, sharpe, max_drawdown,
			win_rate, fitness, generation, status, created_at, updated_at
		FROM strategy_genes
		WHERE generation = $1
		ORDER BY fitness DESC
	`

	rows, err := p.pool.Query(ctx, query, generation)
	if err != nil {
		return nil, fmt.Errorf("list by generation: %w", err)
	}
	defer rows.Close()

	return scanStrategyRows(rows)
}

// UpdateStatus updates the status of a strategy gene.
func (p *StrategyPool) UpdateStatus(ctx context.Context, id string, status string) error {
	query := `UPDATE strategy_genes SET status = $1, updated_at = $2 WHERE id = $3`
	_, err := p.pool.Exec(ctx, query, status, time.Now(), id)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// UpdateMetrics updates the performance metrics of a strategy gene.
func (p *StrategyPool) UpdateMetrics(ctx context.Context, id string, totalReturn, sharpe, maxDrawdown, winRate, fitness float64) error {
	query := `
		UPDATE strategy_genes
		SET total_return = $1, sharpe = $2, max_drawdown = $3, win_rate = $4, fitness = $5, updated_at = $6
		WHERE id = $7
	`
	_, err := p.pool.Exec(ctx, query, totalReturn, sharpe, maxDrawdown, winRate, fitness, time.Now(), id)
	if err != nil {
		return fmt.Errorf("update metrics: %w", err)
	}
	return nil
}

// Delete removes a strategy gene from the pool.
func (p *StrategyPool) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM strategy_genes WHERE id = $1`
	_, err := p.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete strategy gene: %w", err)
	}
	return nil
}

// Count returns the total number of strategy genes.
func (p *StrategyPool) Count(ctx context.Context) (int, error) {
	query := `SELECT COUNT(*) FROM strategy_genes`
	var count int
	err := p.pool.QueryRow(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count strategy genes: %w", err)
	}
	return count, nil
}

// GetTopStrategies returns the top N strategies by fitness.
func (p *StrategyPool) GetTopStrategies(ctx context.Context, n int) ([]*StrategyGene, error) {
	return p.List(ctx, "", "", -1.0, n)
}

// scanStrategyRows scans pgx.Rows into []*StrategyGene.
func scanStrategyRows(rows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
	Close()
}) ([]*StrategyGene, error) {
	var genes []*StrategyGene
	for rows.Next() {
		gene := &StrategyGene{}
		var params, factorIDs, parentIDs []byte

		err := rows.Scan(
			&gene.ID, &gene.Name, &gene.Description, &gene.StrategyType, &gene.Code, &params,
			&factorIDs, &parentIDs, &gene.TotalReturn, &gene.Sharpe, &gene.MaxDrawdown,
			&gene.WinRate, &gene.Fitness, &gene.Generation, &gene.Status, &gene.CreatedAt, &gene.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan strategy gene: %w", err)
		}

		if err := unmarshalStrategyGeneFields(gene, params, factorIDs, parentIDs); err != nil {
			return nil, err
		}

		genes = append(genes, gene)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return genes, nil
}
