package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithSavepoint runs fn inside a SAVEPOINT of the request transaction. The tenant middleware commits the request even
// when a handler answers 4xx, so a handler whose call can fail half way (or on a constraint violation, which aborts the
// transaction) uses this to leave nothing behind and keep the transaction usable for the response.
func WithSavepoint(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context) error) error {
	tx, ok := QuerierFromContext(ctx, pool).(pgx.Tx)
	if !ok {
		return fn(ctx)
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(WithQuerier(ctx, sp)); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	return sp.Commit(ctx)
}
