package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	attendanceports "github.com/omnira/omnira/internal/attendance/ports"
	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresClosingReader reads the last text messages of one conversation of the tenant (explicit tenant filter on top of RLS).
type PostgresClosingReader struct{ pool *pgxpool.Pool }

var _ ports.ClosingReader = (*PostgresClosingReader)(nil)

func NewPostgresClosingReader(pool *pgxpool.Pool) *PostgresClosingReader {
	return &PostgresClosingReader{pool: pool}
}

func (r *PostgresClosingReader) RecentMessages(ctx context.Context, tenantID, conversationID uuid.UUID, limit int) ([]ports.ClosingMessage, error) {
	rows, err := platformdb.QuerierFromContext(ctx, r.pool).Query(ctx, `
		SELECT direction, sent_by_user_id IS NULL, body, created_at
		FROM messages
		WHERE tenant_id = $1 AND conversation_id = $2 AND message_type = 'text' AND btrim(body) <> ''
		ORDER BY created_at DESC, id DESC LIMIT $3`, tenantID, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var newestFirst []ports.ClosingMessage
	for rows.Next() {
		var direction string
		var noUser bool
		var m ports.ClosingMessage
		if err := rows.Scan(&direction, &noUser, &m.Text, &m.At); err != nil {
			return nil, err
		}
		switch {
		case direction == "inbound":
			m.Role = "customer"
		case noUser:
			m.Role = "bot"
		default:
			m.Role = "agent"
		}
		newestFirst = append(newestFirst, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ports.ClosingMessage, 0, len(newestFirst))
	for i := len(newestFirst) - 1; i >= 0; i-- { // oldest first
		out = append(out, newestFirst[i])
	}
	return out, nil
}

// ClosingSuggestAdapter exposes the intelligence suggester to the attendance module, translating its errors.
type ClosingSuggestAdapter struct{ svc *application.ClosingSuggester }

var _ attendanceports.ClosingSuggester = (*ClosingSuggestAdapter)(nil)

func NewClosingSuggestAdapter(svc *application.ClosingSuggester) *ClosingSuggestAdapter {
	return &ClosingSuggestAdapter{svc: svc}
}

func (a *ClosingSuggestAdapter) SuggestClosing(ctx context.Context, conversationID uuid.UUID) (attendancedomain.ClosingSuggestion, error) {
	res, err := a.svc.Suggest(ctx, conversationID)
	switch {
	case errors.Is(err, application.ErrClosingDisabled):
		return attendancedomain.ClosingSuggestion{}, attendancedomain.ErrSuggestionDisabled
	case errors.Is(err, application.ErrClosingNothing):
		return attendancedomain.ClosingSuggestion{}, attendancedomain.ErrNothingToSuggest
	case errors.Is(err, application.ErrClosingUnavailable), errors.Is(err, application.ErrClosingThrottled):
		return attendancedomain.ClosingSuggestion{}, attendancedomain.ErrSuggestionUnavailable
	case err != nil:
		return attendancedomain.ClosingSuggestion{}, err
	}
	out := attendancedomain.ClosingSuggestion{Summary: res.Suggestion.Summary, Model: res.Model, BasedOnMessages: res.BasedOnMessages}
	for _, f := range res.Suggestion.FollowUps {
		out.FollowUps = append(out.FollowUps, attendancedomain.FollowUpInput{Kind: attendancedomain.FollowUpKind(f.Kind), Text: f.Text})
	}
	return out, nil
}
