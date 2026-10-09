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
}

func NewPostgresSessionStore(pool *pgxpool.Pool) *PostgresSessionStore {
	return &PostgresSessionStore{pool: pool}
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
	`, sessionID).Scan(&userID)

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

// UpdateActivity updates last_activity_at for session.
func (s *PostgresSessionStore) UpdateActivity(ctx context.Context, sessionID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE auth_sessions SET last_activity_at=NOW() WHERE id=$1
	`, sessionID)
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
