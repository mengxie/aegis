package usage

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"

	_ "github.com/lib/pq" // PostgreSQL driver for database/sql
)

//go:embed schema.sql
var schema string

// migrationLock serialises schema setup when several gateway replicas start together.
const migrationLock = 0x4145_4749 // "AEGI"

// PostgresStore shares budgets and the audit log across every gateway replica.
type PostgresStore struct{ db *sql.DB }

func OpenPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	store := &PostgresStore{db: db}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return store, nil
}

func (s *PostgresStore) migrate(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLock) //nolint:errcheck
	_, err = conn.ExecContext(ctx, schema)
	return err
}

func (s *PostgresStore) Close() error { return s.db.Close() }

func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *PostgresStore) Spent(ctx context.Context, team, month string) (NanoUSD, error) {
	var spent int64
	err := s.db.QueryRowContext(ctx, "SELECT nano_usd FROM aegis_spend WHERE team = $1 AND month = $2", team, month).Scan(&spent)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return NanoUSD(spent), err
}

// Record writes the log row and adds to the month's spend in one transaction. The upsert is atomic,
// so concurrent replicas never lose an increment.
func (s *PostgresStore) Record(ctx context.Context, r Record) (NanoUSD, error) {
	pii, err := json.Marshal(r.PII)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	_, err = tx.ExecContext(ctx, `
		INSERT INTO aegis_requests (id, at, team, key_id, model, upstream, status, prompt_tokens,
			completion_tokens, nano_usd, cached, pii, decision, latency_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		r.RequestID, r.At, r.Team, r.KeyID, r.Model, r.Upstream, r.Status, r.PromptTokens,
		r.CompletionTokens, int64(r.Cost), r.Cached, string(pii), r.Decision, r.Latency.Milliseconds())
	if err != nil {
		return 0, err
	}
	var total int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO aegis_spend (team, month, nano_usd) VALUES ($1, $2, $3)
		ON CONFLICT (team, month) DO UPDATE SET nano_usd = aegis_spend.nano_usd + EXCLUDED.nano_usd
		RETURNING nano_usd`, r.Team, Month(r.At), int64(r.Cost)).Scan(&total)
	if err != nil {
		return 0, err
	}
	return NanoUSD(total), tx.Commit()
}
