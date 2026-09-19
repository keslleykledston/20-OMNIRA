// Package db fornece o mecanismo de sessão tenant-aware que a RLS do
// PostgreSQL depende: cada request autenticado precisa rodar suas queries
// numa transação onde `app.current_user_id` foi setado via set_config(...,
// true) (equivalente a SET LOCAL, dura só a transação corrente).
//
// Sem isso, current_user_id() no banco retorna NULL e toda policy RLS que
// depende dela nega acesso (fail-closed) — correto para segurança, mas
// inútil para servir requests legítimos. Ver migrations 000004/000005/000006
// e o ADR relacionado para o histórico de por que isso é necessário mesmo
// com RLS habilitada.
package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier — subconjunto comum de *pgxpool.Pool e pgx.Tx usado pelos
// repositórios. Permite que o mesmo código de repositório opere tanto numa
// conexão de pool "solta" (sem tenant context, ex.: login) quanto dentro de
// uma transação com app.current_user_id setado.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type querierCtxKey struct{}

// WithQuerier — injeta um Querier (tipicamente uma pgx.Tx) no context.
func WithQuerier(ctx context.Context, q Querier) context.Context {
	return context.WithValue(ctx, querierCtxKey{}, q)
}

// QuerierFromContext — recupera o Querier injetado no context, ou retorna
// fallback (o pool global) se nenhum foi injetado. Repositórios devem sempre
// chamar isto em vez de usar seu *pgxpool.Pool diretamente.
func QuerierFromContext(ctx context.Context, fallback Querier) Querier {
	if q, ok := ctx.Value(querierCtxKey{}).(Querier); ok {
		return q
	}
	return fallback
}

// WithTenantSession — adquire uma conexão dedicada do pool, abre uma
// transação, seta app.current_user_id (e app.is_system_admin quando
// isSystemAdmin=true) via set_config com is_local=true, injeta a transação
// no context e executa fn. Commita ao final se fn não retornar erro.
//
// Uma conexão exclusiva é necessária porque SET LOCAL/set_config(...,true)
// só tem efeito dentro da transação da conexão corrente — reusar o pool sem
// isolar a conexão vazaria a GUC entre requests concorrentes.
func WithTenantSession(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, isSystemAdmin bool, fn func(ctx context.Context) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	// Rollback é no-op se a transação já foi commitada.
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_user_id', $1, true)`, userID.String()); err != nil {
		return err
	}
	if isSystemAdmin {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
			return err
		}
	}

	if err := fn(WithQuerier(ctx, tx)); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
