package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/domain"
	"github.com/omnira/omnira/internal/routing/ports"
)

type PostgresParticipantRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresParticipantRepository(pool *pgxpool.Pool) ports.ParticipantRepository {
	return &PostgresParticipantRepository{pool: pool}
}

func (r *PostgresParticipantRepository) Create(ctx context.Context, p *domain.ConversationParticipant) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx, `
		INSERT INTO conversation_participants (id, tenant_id, conversation_id, user_id, role, joined_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (conversation_id, user_id) DO NOTHING
	`, p.ID, p.TenantID, p.ConversationID, p.UserID, p.Role, p.JoinedAt, p.CreatedAt, p.UpdatedAt)
	return err
}

func (r *PostgresParticipantRepository) FindByConversation(ctx context.Context, conversationID uuid.UUID) ([]*domain.ConversationParticipant, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx, `
		SELECT id, tenant_id, conversation_id, user_id, role, joined_at, left_at, created_at, updated_at
		FROM conversation_participants
		WHERE conversation_id = $1
		ORDER BY created_at ASC
	`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var participants []*domain.ConversationParticipant
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		participants = append(participants, p)
	}
	return participants, rows.Err()
}

func (r *PostgresParticipantRepository) FindAssignee(ctx context.Context, conversationID uuid.UUID) (*domain.ConversationParticipant, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	row := q.QueryRow(ctx, `
		SELECT id, tenant_id, conversation_id, user_id, role, joined_at, left_at, created_at, updated_at
		FROM conversation_participants
		WHERE conversation_id = $1 AND role = 'ASSIGNEE'
		LIMIT 1
	`, conversationID)
	p, err := scanParticipant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (r *PostgresParticipantRepository) FindByUserAndConversation(ctx context.Context, conversationID, userID uuid.UUID) (*domain.ConversationParticipant, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	row := q.QueryRow(ctx, `
		SELECT id, tenant_id, conversation_id, user_id, role, joined_at, left_at, created_at, updated_at
		FROM conversation_participants
		WHERE conversation_id = $1 AND user_id = $2
		LIMIT 1
	`, conversationID, userID)
	p, err := scanParticipant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (r *PostgresParticipantRepository) UpdateRole(ctx context.Context, participantID uuid.UUID, role domain.ParticipantRole) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx, `
		UPDATE conversation_participants
		SET role = $2, updated_at = now()
		WHERE id = $1
	`, participantID, role)
	return err
}

func (r *PostgresParticipantRepository) MarkAsJoined(ctx context.Context, participantID uuid.UUID) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx, `
		UPDATE conversation_participants
		SET joined_at = now(), updated_at = now()
		WHERE id = $1 AND joined_at IS NULL
	`, participantID)
	return err
}

func (r *PostgresParticipantRepository) MarkAsLeft(ctx context.Context, participantID uuid.UUID) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx, `
		UPDATE conversation_participants
		SET left_at = now(), updated_at = now()
		WHERE id = $1 AND left_at IS NULL
	`, participantID)
	return err
}

func (r *PostgresParticipantRepository) ListActiveParticipants(ctx context.Context, conversationID uuid.UUID) ([]*domain.ConversationParticipant, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx, `
		SELECT id, tenant_id, conversation_id, user_id, role, joined_at, left_at, created_at, updated_at
		FROM conversation_participants
		WHERE conversation_id = $1 AND left_at IS NULL
		ORDER BY created_at ASC
	`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var participants []*domain.ConversationParticipant
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		participants = append(participants, p)
	}
	return participants, rows.Err()
}

type participantScanner interface {
	Scan(...any) error
}

func scanParticipant(row participantScanner) (*domain.ConversationParticipant, error) {
	p := &domain.ConversationParticipant{}
	var role string
	err := row.Scan(&p.ID, &p.TenantID, &p.ConversationID, &p.UserID, &role, &p.JoinedAt, &p.LeftAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.Role = domain.ParticipantRole(role)
	return p, nil
}
