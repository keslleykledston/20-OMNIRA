package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/channels/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type PostgresOutboundStore struct{ pool *pgxpool.Pool }

var _ OutboundStore = (*PostgresOutboundStore)(nil)

func NewPostgresOutboundStore(pool *pgxpool.Pool) *PostgresOutboundStore {
	return &PostgresOutboundStore{pool: pool}
}

func (s *PostgresOutboundStore) RunForMessage(ctx context.Context, messageID uuid.UUID, fn func(context.Context) error) error {
	if s == nil || s.pool == nil || messageID == uuid.Nil || fn == nil {
		return fmt.Errorf("%w: invalid message runner input", ErrPermanent)
	}
	var tenantID uuid.UUID
	err := platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(system context.Context) error {
		return platformdb.QuerierFromContext(system, s.pool).QueryRow(system,
			`SELECT tenant_id FROM messages WHERE id=$1 AND direction='outbound'`, messageID).Scan(&tenantID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: unknown message", ErrPermanent)
	}
	if err != nil {
		return err
	}
	return platformdb.WithSystemTenantSession(ctx, s.pool, tenantID, fn)
}

func tenantOf(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("channel delivery: tenant context required")
	}
	return tc.TenantID, nil
}

func (s *PostgresOutboundStore) LockOutbound(ctx context.Context, messageID uuid.UUID) (*OutboundJob, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	job := &OutboundJob{MessageID: messageID}
	var connection *uuid.UUID
	var active *bool
	var phone *string
	var tplName, tplLang *string
	var tplParams []byte
	var itxBody, itxLabel *string
	var itxOptions []byte
	var mediaID *uuid.UUID
	var mediaKind, mediaMime, mediaName, mediaSHA *string
	var mediaSize *int64
	err = platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT m.channel_connection_id, ct.phone_e164, c.provider_chat_id, m.body, m.status,
		       m.reserved_provider_message_id, m.provider_message_id, (cc.status = 'active'),
		       ts.template_name, ts.language, ts.params, isn.body, isn.list_label, isn.options,
		       om.id, om.kind, om.mime, om.file_name, om.sha256, om.size_bytes
		FROM messages m
		JOIN conversations c ON c.tenant_id = m.tenant_id AND c.id = m.conversation_id
		JOIN contacts ct ON ct.tenant_id = c.tenant_id AND ct.id = c.contact_id
		LEFT JOIN channel_connections cc ON cc.tenant_id = m.tenant_id AND cc.id = m.channel_connection_id
		LEFT JOIN message_template_sends ts ON ts.tenant_id = m.tenant_id AND ts.message_id = m.id
		LEFT JOIN message_interactive_sends isn ON isn.tenant_id = m.tenant_id AND isn.message_id = m.id
		LEFT JOIN message_outbound_media om ON om.tenant_id = m.tenant_id AND om.message_id = m.id
		WHERE m.tenant_id = $1 AND m.id = $2 AND m.direction = 'outbound'
		FOR UPDATE OF m`, tenantID, messageID).
		Scan(&connection, &phone, &job.ProviderChatID, &job.Text, &job.Status, &job.ReservedProviderMessageID, &job.ProviderMessageID, &active,
			&tplName, &tplLang, &tplParams, &itxBody, &itxLabel, &itxOptions,
			&mediaID, &mediaKind, &mediaMime, &mediaName, &mediaSHA, &mediaSize)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("channel delivery: lock message: %w", err)
	}
	if connection != nil {
		job.ConnectionID = *connection
	}
	if phone != nil {
		job.ToE164 = *phone
	}
	job.ConnectionActive = active != nil && *active
	// The company must still be active, and stay so until this transaction ends (share lock on its row).
	tenantActive, err := platformdb.LockTenantActive(ctx, platformdb.QuerierFromContext(ctx, s.pool), tenantID)
	if err != nil {
		return nil, fmt.Errorf("channel delivery: check company status: %w", err)
	}
	job.TenantSuspended = !tenantActive
	if tplName != nil && tplLang != nil {
		t := &TemplateJob{Name: *tplName, Language: *tplLang}
		if err := json.Unmarshal(tplParams, &t.Params); err != nil {
			return nil, fmt.Errorf("%w: malformed template params", ErrPermanent)
		}
		job.Template = t
	}
	if mediaID != nil && mediaKind != nil && mediaMime != nil && mediaSHA != nil && mediaSize != nil {
		j := &MediaJob{AttachmentID: *mediaID, TenantID: tenantID, Kind: *mediaKind, Mime: *mediaMime, SHA256: *mediaSHA, Size: *mediaSize}
		if mediaName != nil {
			j.FileName = *mediaName
		}
		job.Media = j
	}
	if itxBody != nil && len(itxOptions) > 0 {
		var raw []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal(itxOptions, &raw); err != nil {
			return nil, fmt.Errorf("%w: malformed interactive options", ErrPermanent)
		}
		j := &InteractiveJob{Body: *itxBody}
		if itxLabel != nil {
			j.ListLabel = *itxLabel
		}
		for _, o := range raw {
			j.Options = append(j.Options, domain.InteractiveOption{ID: o.ID, Title: o.Title})
		}
		job.Interactive = j
	}
	return job, nil
}

// EnsureReservedProviderMessageID implements the store side of PILOT.4A1's
// central durability property: the reservation is committed in its own
// transaction, independent of (and always before) any transaction that calls
// the provider. See the interface doc in send.go for the race-safety
// contract.
func (s *PostgresOutboundStore) EnsureReservedProviderMessageID(ctx context.Context, messageID uuid.UUID, generate func(context.Context) (string, error)) (string, error) {
	tenantID, existing, err := s.readReservation(ctx, messageID)
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}

	// Network call to the provider happens OUTSIDE any lock/transaction —
	// new-message-id has no delivery side effect, so a wasted candidate from
	// a losing race is harmless (PILOT.4A1 §7).
	candidate, err := generate(ctx)
	if err != nil {
		return "", err
	}
	if candidate == "" {
		return "", fmt.Errorf("%w: provider returned an empty reserved message id", ErrPermanent)
	}

	won, err := s.tryPersistReservation(ctx, tenantID, messageID, candidate)
	if err != nil {
		return "", err
	}
	if won {
		return candidate, nil
	}

	// Lost the race: another delivery attempt persisted first. Read and
	// reuse its value rather than proceeding with our own candidate.
	_, winner, err := s.readReservation(ctx, messageID)
	if err != nil {
		return "", err
	}
	if winner == "" {
		return "", errors.New("channel delivery: reservation vanished after a lost race")
	}
	return winner, nil
}

func (s *PostgresOutboundStore) readReservation(ctx context.Context, messageID uuid.UUID) (uuid.UUID, string, error) {
	var tenantID uuid.UUID
	var reserved string
	err := platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(system context.Context) error {
		return platformdb.QuerierFromContext(system, s.pool).QueryRow(system,
			`SELECT tenant_id, reserved_provider_message_id FROM messages WHERE id=$1 AND direction='outbound'`,
			messageID).Scan(&tenantID, &reserved)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", fmt.Errorf("%w: unknown message", ErrPermanent)
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("channel delivery: read reservation: %w", err)
	}
	return tenantID, reserved, nil
}

func (s *PostgresOutboundStore) tryPersistReservation(ctx context.Context, tenantID, messageID uuid.UUID, candidate string) (bool, error) {
	var won bool
	err := platformdb.WithSystemTenantSession(ctx, s.pool, tenantID, func(scoped context.Context) error {
		if active, err := platformdb.LockTenantActive(scoped, platformdb.QuerierFromContext(scoped, s.pool), tenantID); err != nil {
			return err
		} else if !active {
			return ErrTenantSuspended
		}
		tag, err := platformdb.QuerierFromContext(scoped, s.pool).Exec(scoped, `
			UPDATE messages SET reserved_provider_message_id=$3, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND reserved_provider_message_id=''`,
			tenantID, messageID, candidate)
		if err != nil {
			return err
		}
		won = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("channel delivery: persist reservation: %w", err)
	}
	return won, nil
}

func (s *PostgresOutboundStore) MarkSent(ctx context.Context, messageID uuid.UUID, providerMessageID string) error {
	return s.finish(ctx, messageID, `UPDATE messages SET status='sent', provider_message_id=$3, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status='queued'`, providerMessageID)
}

func (s *PostgresOutboundStore) MarkFailed(ctx context.Context, messageID uuid.UUID, reason string) error {
	return s.finish(ctx, messageID, `UPDATE messages SET status='failed', failure_reason=$3, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status='queued'`, reason)
}

// MarkUncertain (PILOT.4A2) records an unproven provider outcome.
// provider_message_id is deliberately left untouched by this statement
// (stays empty, since no confirmed success occurred) and
// reserved_provider_message_id is never cleared — see the OutboundStore
// interface doc.
func (s *PostgresOutboundStore) MarkUncertain(ctx context.Context, messageID uuid.UUID, reason string) error {
	return s.finish(ctx, messageID, `UPDATE messages SET status='uncertain', failure_reason=$3, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status='queued'`, reason)
}

func (s *PostgresOutboundStore) finish(ctx context.Context, messageID uuid.UUID, sql, value string) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	tag, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, sql, tenantID, messageID, value)
	if err != nil {
		return fmt.Errorf("channel delivery: update message: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("channel delivery: message was not queued")
	}
	return nil
}
