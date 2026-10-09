package gene_pool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FactorGene represents a single factor in the gene pool.
type FactorGene struct {
	ID          string    `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Category    string    `json:"category" db:"category"`
	Formula     string    `json:"formula" db:"formula"`
	Description string    `json:"description" db:"description"`
	Rationale   string    `json:"rationale" db:"rationale"`
	IC          float64   `json:"ic" db:"ic"`
	IR          float64   `json:"ir" db:"ir"`
	Turnover    float64   `json:"turnover" db:"turnover"`
	Sharpe      float64   `json:"sharpe" db:"sharpe"`
	Fitness     float64   `json:"fitness" db:"fitness"`
	Generation  int       `json:"generation" db:"generation"`
	ParentIDs   []string  `json:"parent_ids" db:"parent_ids"`
	Status      string    `json:"status" db:"status"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}

// FactorPool manages factor genes in PostgreSQL.
type FactorPool struct {
	pool *pgxpool.Pool
}

// NewFactorPool creates a new FactorPool.
func NewFactorPool(pool *pgxpool.Pool) *FactorPool {
	return &FactorPool{pool: pool}
}

// Save persists a factor gene to the database.
func (p *FactorPool) Save(ctx context.Context, gene *FactorGene) error {
	parentIDs, err := json.Marshal(gene.ParentIDs)
	if err != nil {
		return fmt.Errorf("marshal parent_ids: %w", err)
	}

	query := `
		INSERT INTO factor_genes (
			id, name, category, formula, description, rationale,
			ic, ir, turnover, sharpe, fitness, generation,
			parent_ids, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			category = EXCLUDED.category,
			formula = EXCLUDED.formula,
			description = EXCLUDED.description,
			rationale = EXCLUDED.rationale,
			ic = EXCLUDED.ic,
			ir = EXCLUDED.ir,
			turnover = EXCLUDED.turnover,
			sharpe = EXCLUDED.sharpe,
			fitness = EXCLUDED.fitness,
			generation = EXCLUDED.generation,
			parent_ids = EXCLUDED.parent_ids,
			status = EXCLUDED.status,
			updated_at = EXCLUDED.updated_at
	`

	_, err = p.pool.Exec(ctx, query,
		gene.ID, gene.Name, gene.Category, gene.Formula,
		gene.Description, gene.Rationale, gene.IC, gene.IR,
		gene.Turnover, gene.Sharpe, gene.Fitness, gene.Generation,
		parentIDs, gene.Status, gene.CreatedAt, gene.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("save factor gene: %w", err)
	}
	return nil
}

// Get retrieves a factor gene by ID.
func (p *FactorPool) Get(ctx context.Context, id string) (*FactorGene, error) {
	query := `
		SELECT id, name, category, formula, description, rationale,
			ic, ir, turnover, sharpe, fitness, generation,
			parent_ids, status, created_at, updated_at
		FROM factor_genes WHERE id = $1
	`

	row := p.pool.QueryRow(ctx, query, id)
	gene := &FactorGene{}
	var parentIDs []byte

	err := row.Scan(
		&gene.ID, &gene.Name, &gene.Category, &gene.Formula,
		&gene.Description, &gene.Rationale, &gene.IC, &gene.IR,
		&gene.Turnover, &gene.Sharpe, &gene.Fitness, &gene.Generation,
		&parentIDs, &gene.Status, &gene.CreatedAt, &gene.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get factor gene: %w", err)
	}

	if err := unmarshalFactorGeneFields(gene, parentIDs); err != nil {
		return nil, err
	}

	return gene, nil
}

// unmarshalFactorGeneFields parses the parent_ids JSON blob column
// scanned from the factor_genes table into the gene struct. An empty
// blob is skipped. Returns an error if the blob is non-empty but
// malformed JSON, so callers don't silently get a gene with zero-valued
// ParentIDs.
//
// S7-P0-6 (ODR-043): previously the unmarshal error was discarded,
// masking data corruption.
func unmarshalFactorGeneFields(gene *FactorGene, parentIDs []byte) error {
	if len(parentIDs) > 0 {
		if err := json.Unmarshal(parentIDs, &gene.ParentIDs); err != nil {
			return fmt.Errorf("parse factor gene parent_ids: %w", err)
		}
	}
	return nil
}

// List retrieves factor genes with optional filters.
func (p *FactorPool) List(ctx context.Context, category string, status string, minIC float64, limit int) ([]*FactorGene, error) {
	query := `
		SELECT id, name, category, formula, description, rationale,
			ic, ir, turnover, sharpe, fitness, generation,
			parent_ids, status, created_at, updated_at
		FROM factor_genes
		WHERE ($1 = '' OR category = $1)
			AND ($2 = '' OR status = $2)
			AND ic >= $3
		ORDER BY fitness DESC, ic DESC
		LIMIT $4
	`

	rows, err := p.pool.Query(ctx, query, category, status, minIC, limit)
	if err != nil {
		return nil, fmt.Errorf("list factor genes: %w", err)
	}
	defer rows.Close()

	return scanFactorRows(rows)
}

// ListByGeneration retrieves factor genes for a specific generation.
func (p *FactorPool) ListByGeneration(ctx context.Context, generation int) ([]*FactorGene, error) {
	query := `
		SELECT id, name, category, formula, description, rationale,
			ic, ir, turnover, sharpe, fitness, generation,
			parent_ids, status, created_at, updated_at
		FROM factor_genes
		WHERE generation = $1
		ORDER BY fitness DESC
	`

	rows, err := p.pool.Query(ctx, query, generation)
	if err != nil {
		return nil, fmt.Errorf("list by generation: %w", err)
	}
	defer rows.Close()

	return scanFactorRows(rows)
}

// UpdateStatus updates the status of a factor gene.
func (p *FactorPool) UpdateStatus(ctx context.Context, id string, status string) error {
	query := `UPDATE factor_genes SET status = $1, updated_at = $2 WHERE id = $3`
	_, err := p.pool.Exec(ctx, query, status, time.Now(), id)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// UpdateMetrics updates the metrics (IC, IR, etc.) of a factor gene.
func (p *FactorPool) UpdateMetrics(ctx context.Context, id string, ic, ir, turnover, sharpe, fitness float64) error {
	query := `
		UPDATE factor_genes
		SET ic = $1, ir = $2, turnover = $3, sharpe = $4, fitness = $5, updated_at = $6
		WHERE id = $7
	`
	_, err := p.pool.Exec(ctx, query, ic, ir, turnover, sharpe, fitness, time.Now(), id)
	if err != nil {
		return fmt.Errorf("update metrics: %w", err)
	}
	return nil
}

// Delete removes a factor gene from the pool.
func (p *FactorPool) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM factor_genes WHERE id = $1`
	_, err := p.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete factor gene: %w", err)
	}
	return nil
}

// Count returns the total number of factor genes.
func (p *FactorPool) Count(ctx context.Context) (int, error) {
	query := `SELECT COUNT(*) FROM factor_genes`
	var count int
	err := p.pool.QueryRow(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count factor genes: %w", err)
	}
	return count, nil
}

// CountByStatus returns the number of factor genes by status.
func (p *FactorPool) CountByStatus(ctx context.Context, status string) (int, error) {
	query := `SELECT COUNT(*) FROM factor_genes WHERE status = $1`
	var count int
	err := p.pool.QueryRow(ctx, query, status).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count by status: %w", err)
	}
	return count, nil
}

// GetTopFactors returns the top N factors by fitness.
func (p *FactorPool) GetTopFactors(ctx context.Context, n int) ([]*FactorGene, error) {
	return p.List(ctx, "", "", -1.0, n)
}

// GetCategories returns all distinct factor categories.
func (p *FactorPool) GetCategories(ctx context.Context) ([]string, error) {
	query := `SELECT DISTINCT category FROM factor_genes ORDER BY category`
	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("get categories: %w", err)
	}
	defer rows.Close()

	var categories []string
	for rows.Next() {
		var cat string
		if err := rows.Scan(&cat); err != nil {
			return nil, err
		}
		categories = append(categories, cat)
	}
	return categories, rows.Err()
}

// scanFactorRows scans pgx.Rows into []*FactorGene.
func scanFactorRows(rows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
	Close()
}) ([]*FactorGene, error) {
	var genes []*FactorGene
	for rows.Next() {
		gene := &FactorGene{}
		var parentIDs []byte

		err := rows.Scan(
			&gene.ID, &gene.Name, &gene.Category, &gene.Formula,
			&gene.Description, &gene.Rationale, &gene.IC, &gene.IR,
			&gene.Turnover, &gene.Sharpe, &gene.Fitness, &gene.Generation,
			&parentIDs, &gene.Status, &gene.CreatedAt, &gene.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan factor gene: %w", err)
		}

		if err := unmarshalFactorGeneFields(gene, parentIDs); err != nil {
			return nil, err
		}

		genes = append(genes, gene)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return genes, nil
}
