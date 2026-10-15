package authn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionStore manages server-side browser sessions (opaque session IDs, not tokens).
type SessionStore interface {
	CreateSession(ctx context.Context, userID uuid.UUID, authMethod string, ttl time.Duration) (string, error)
	ResolveSession(ctx context.Context, sessionID string) (uuid.UUID, error)
	RevokeSession(ctx context.Context, sessionID string) error
	UpdateActivity(ctx context.Context, sessionID string) error
}

type PostgresSessionStore struct {
	pool *pgxpool.Pool
	// idle > 0: each request a session serves pushes its expiry to now+idle, never past created_at+max (max > 0 = ceiling).
	// idle = 0: the expiry stays where CreateSession put it.
	idle, max time.Duration
}

func NewPostgresSessionStore(pool *pgxpool.Pool) *PostgresSessionStore {
	return &PostgresSessionStore{pool: pool}
}

// WithLifetime makes browser sessions end after idle without use (each use pushes the expiry forward) and never later than max after
// sign-in (0 = no ceiling). A stream (ValidateSession) does not count as use.
func (s *PostgresSessionStore) WithLifetime(idle, max time.Duration) *PostgresSessionStore {
	s.idle, s.max = idle, max
	return s
}

// CreateSession generates opaque session ID and stores user_id server-side.
func (s *PostgresSessionStore) CreateSession(ctx context.Context, userID uuid.UUID, authMethod string, ttl time.Duration) (string, error) {
	sessionID := generateSessionID()
	expiresAt := time.Now().Add(ttl)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth_sessions(id, user_id, auth_method, expires_at)
		VALUES ($1, $2, $3, $4)
	`, sessionID, userID, authMethod, expiresAt)

	if err != nil {
		return "", err
	}

	return sessionID, nil
}

// ResolveSession validates session and returns user_id if valid.
func (s *PostgresSessionStore) ResolveSession(ctx context.Context, sessionID string) (uuid.UUID, error) {
	var userID uuid.UUID

	err := s.pool.QueryRow(ctx, `
		SELECT user_id FROM auth_sessions
		WHERE id=$1 AND revoked_at IS NULL AND expires_at > NOW() AND session_account_active(id)
		  AND ($2::double precision <= 0 OR created_at + make_interval(secs => $2::double precision) > NOW())
	`, sessionID, s.max.Seconds()).Scan(&userID)

	if err == pgx.ErrNoRows {
		return uuid.Nil, errors.New("session not found or expired")
	}
	if err != nil {
		return uuid.Nil, err
	}

	// Update activity
	_ = s.UpdateActivity(ctx, sessionID)

	return userID, nil
}

// RevokeSession marks session as revoked (logout).
func (s *PostgresSessionStore) RevokeSession(ctx context.Context, sessionID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at=NOW() WHERE id=$1
	`, sessionID)
	return err
}

// UpdateActivity records the use of a session and, with an idle lifetime, pushes its expiry to now+idle: never backwards, never past
// the ceiling, never for a session already revoked or expired.
func (s *PostgresSessionStore) UpdateActivity(ctx context.Context, sessionID string) error {
	if s.idle <= 0 {
		_, err := s.pool.Exec(ctx, `
			UPDATE auth_sessions SET last_activity_at=NOW() WHERE id=$1
		`, sessionID)
		return err
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE auth_sessions
		SET last_activity_at = NOW(),
		    expires_at = LEAST(
		        GREATEST(expires_at, NOW() + make_interval(secs => $2::double precision)),
		        CASE WHEN $3::double precision > 0 THEN created_at + make_interval(secs => $3::double precision)
		             ELSE 'infinity'::timestamptz END)
		WHERE id=$1 AND revoked_at IS NULL AND expires_at > NOW()
	`, sessionID, s.idle.Seconds(), s.max.Seconds())
	return err
}

// generateSessionID produces a 32-byte random hex string (opaque, unguessable).
func generateSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // rand.Read should never fail
	}
	return hex.EncodeToString(b)
}
