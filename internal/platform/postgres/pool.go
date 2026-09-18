// Package postgres holds every piece of code that knows PostgreSQL exists:
// the connection pool, migrations runner, and the repositories that
// translate between domain types (Wallet, WagerTransaction, LedgerEntry)
// and SQL rows. The domain packages never import this one - only the other
// direction.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a connection pool against dsn (a standard
// postgres://user:pass@host:port/db URL). Callers are responsible for
// calling Close on the returned pool during shutdown.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx, letting every
// repository method accept either a bare pool connection (for reads) or an
// in-flight transaction (for the multi-table writes that must commit
// atomically) without knowing which one it got.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
