// Package adapters connects the group copilot (ADR-0050) to PostgreSQL, to the HTTP layer, to the antivirus and to the service module.
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/groupai/application"
	"github.com/omnira/omnira/internal/groupai/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Store is the PostgreSQL implementation of application.Store. Every method runs on the querier of the context (a tenant session in the
// API, a system tenant session in the worker); the three cross-tenant listings open their own system session. RLS scopes every row.
type Store struct{ pool *pgxpool.Pool }

var _ application.Store = (*Store)(nil)

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, s.pool)
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// ---- groups and messages

func (s *Store) Group(ctx context.Context, tenantID, groupID uuid.UUID) (application.GroupInfo, error) {
	g := application.GroupInfo{ID: groupID, TenantID: tenantID}
	err := s.q(ctx).QueryRow(ctx, `SELECT name, enabled, ai_enabled FROM wa_groups WHERE tenant_id = $1 AND id = $2`, tenantID, groupID).Scan(&g.Name, &g.Enabled, &g.AIEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, application.ErrNotFound
	}
	return g, err
}

func (s *Store) SetAIEnabled(ctx context.Context, tenantID, groupID uuid.UUID, enabled bool, by uuid.UUID) error {
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE wa_groups SET ai_enabled = $3, updated_at = now(),
		  ai_enabled_by = CASE WHEN $3 THEN $4::uuid ELSE ai_enabled_by END,
		  ai_enabled_at = CASE WHEN $3 THEN now() ELSE ai_enabled_at END
		WHERE tenant_id = $1 AND id = $2`, tenantID, groupID, enabled, by)
	if err == nil && tag.RowsAffected() == 0 {
		return application.ErrNotFound
	}
	return err
}

func (s *Store) MessagesAfter(ctx context.Context, tenantID, groupID uuid.UUID, afterAt *time.Time, afterID *uuid.UUID, limit int) ([]application.GroupMessage, error) {
	args := []any{tenantID, groupID}
	// a message is read once it has settled: the intake commits a moment after it stamps created_at, and a transaction that started earlier but
	// committed later than a read would otherwise be skipped for good by the cursor
	where := "tenant_id = $1 AND group_id = $2 AND created_at <= now() - interval '20 seconds'"
	if afterAt != nil {
		id := uuid.Nil
		if afterID != nil {
			id = *afterID
		}
		args = append(args, *afterAt, id)
		where += " AND (created_at, id) > ($3, $4)"
	}
	args = append(args, limit)
	rows, err := s.q(ctx).Query(ctx, `SELECT id, provider_message_id, author_name, from_me, message_type, body, sent_at, created_at
		FROM wa_group_messages WHERE `+where+` ORDER BY created_at, id LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []application.GroupMessage
	for rows.Next() {
		var m application.GroupMessage
		if err := rows.Scan(&m.ID, &m.ProviderMessageID, &m.Author, &m.FromMe, &m.Type, &m.Body, &m.SentAt, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) CountMessagesAfter(ctx context.Context, tenantID, groupID uuid.UUID, afterAt *time.Time) (int, *time.Time, error) {
	var n int
	var oldest *time.Time
	var err error
	if afterAt == nil {
		err = s.q(ctx).QueryRow(ctx, `SELECT count(*), min(created_at) FROM wa_group_messages WHERE tenant_id = $1 AND group_id = $2 AND created_at <= now() - interval '20 seconds'`, tenantID, groupID).Scan(&n, &oldest)
	} else {
		err = s.q(ctx).QueryRow(ctx, `SELECT count(*), min(created_at) FROM wa_group_messages WHERE tenant_id = $1 AND group_id = $2 AND created_at > $3 AND created_at <= now() - interval '20 seconds'`, tenantID, groupID, *afterAt).Scan(&n, &oldest)
	}
	return n, oldest, err
}

func (s *Store) MessageKeys(ctx context.Context, tenantID, groupID uuid.UUID, from, to time.Time) (map[string]bool, error) {
	rows, err := s.q(ctx).Query(ctx, `SELECT author_name, sent_at, body FROM wa_group_messages WHERE tenant_id = $1 AND group_id = $2 AND sent_at BETWEEN $3 AND $4`, tenantID, groupID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var author, body string
		var at time.Time
		if err := rows.Scan(&author, &at, &body); err != nil {
			return nil, err
		}
		keys[domain.MessageKey(author, at, body)] = true
	}
	return keys, rows.Err()
}

// ---- memory items

const itemCols = `id, tenant_id, group_id, kind, tier, status, truth_level, summary, actor_label, evidence, pinned, relevance, last_reinforced_at, source,
	source_import_id, prompt_version, superseded_by, fingerprint, version, created_at, updated_at`

func scanItem(row pgx.Row) (domain.Item, error) {
	var it domain.Item
	var ev []byte
	var kind, tier, status, truth, source string
	var rel float32
	if err := row.Scan(&it.ID, &it.TenantID, &it.GroupID, &kind, &tier, &status, &truth, &it.Summary, &it.ActorLabel, &ev, &it.Pinned, &rel, &it.LastReinforcedAt,
		&source, &it.SourceImportID, &it.PromptVersion, &it.SupersededBy, &it.Fingerprint, &it.Version, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return it, err
	}
	it.Kind, it.Tier, it.Status, it.Truth, it.Source, it.Relevance = domain.Kind(kind), domain.Tier(tier), domain.Status(status), domain.TruthLevel(truth), domain.Source(source), float64(rel)
	if len(ev) > 0 {
		if err := json.Unmarshal(ev, &it.Evidence); err != nil {
			return it, err
		}
	}
	return it, nil
}

func evidenceJSON(ev []domain.Evidence) []byte {
	if len(ev) == 0 {
		return []byte("[]")
	}
	b, _ := json.Marshal(ev)
	return b
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Store) ListItems(ctx context.Context, tenantID, groupID uuid.UUID, f application.ItemFilter) ([]domain.Item, error) {
	args := []any{tenantID, groupID}
	where := "tenant_id = $1 AND group_id = $2"
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args)))
	}
	if len(f.Status) > 0 {
		st := make([]string, len(f.Status))
		for i, v := range f.Status {
			st[i] = string(v)
		}
		add("status = ANY(?)", st)
	} else if !f.IncludeSuper {
		where += " AND status <> 'superseded'"
	}
	if len(f.Kinds) > 0 {
		ks := make([]string, len(f.Kinds))
		for i, v := range f.Kinds {
			ks[i] = string(v)
		}
		add("kind = ANY(?)", ks)
	}
	if f.Tier != "" {
		add("tier = ?", string(f.Tier))
	}
	if f.Query != "" {
		add(`summary ILIKE '%' || ? || '%' ESCAPE '\'`, escapeLike(f.Query))
	}
	order := "last_reinforced_at DESC, id"
	if f.OldestFirst {
		order = "last_reinforced_at, id"
	}
	if f.OrderByRank {
		// the weight now: stored relevance halved every 60 days since it was last reinforced; what a person pinned comes first
		order = "pinned DESC, (relevance * power(0.5, extract(epoch FROM (now() - last_reinforced_at)) / 5184000.0)) DESC, last_reinforced_at DESC, id"
	}
	limit := f.Limit
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := s.q(ctx).Query(ctx, `SELECT `+itemCols+` FROM wa_group_memory_items WHERE `+where+` ORDER BY `+order+` LIMIT `+fmt.Sprint(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) GetItem(ctx context.Context, tenantID, itemID uuid.UUID, forUpdate bool) (domain.Item, error) {
	q := `SELECT ` + itemCols + ` FROM wa_group_memory_items WHERE tenant_id = $1 AND id = $2`
	if forUpdate {
		q += " FOR UPDATE"
	}
	it, err := scanItem(s.q(ctx).QueryRow(ctx, q, tenantID, itemID))
	if errors.Is(err, pgx.ErrNoRows) {
		return it, application.ErrNotFound
	}
	return it, err
}

func (s *Store) InsertItem(ctx context.Context, it domain.Item) (domain.Item, bool, error) {
	if it.ID == uuid.Nil {
		it.ID = uuid.New()
	}
	var id uuid.UUID
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO wa_group_memory_items (id, tenant_id, group_id, kind, tier, status, truth_level, summary, actor_label, evidence, pinned, relevance,
		  last_reinforced_at, source, source_import_id, prompt_version, fingerprint)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (tenant_id, group_id, fingerprint) WHERE status <> 'superseded' DO NOTHING
		RETURNING id`,
		it.ID, it.TenantID, it.GroupID, string(it.Kind), string(it.Tier), string(it.Status), string(it.Truth), it.Summary, it.ActorLabel, evidenceJSON(it.Evidence),
		it.Pinned, float32(it.Relevance), it.LastReinforcedAt, string(it.Source), it.SourceImportID, it.PromptVersion, it.Fingerprint).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) { // an equal live item exists
		existing, gerr := scanItem(s.q(ctx).QueryRow(ctx, `SELECT `+itemCols+` FROM wa_group_memory_items
			WHERE tenant_id = $1 AND group_id = $2 AND fingerprint = $3 AND status <> 'superseded' FOR UPDATE`, it.TenantID, it.GroupID, it.Fingerprint))
		return existing, false, gerr
	}
	if err != nil {
		return it, false, err
	}
	stored, err := s.GetItem(ctx, it.TenantID, id, false)
	return stored, true, err
}

func (s *Store) UpdateItem(ctx context.Context, it domain.Item, expectedVersion int) error {
	// look before writing: a unique violation would abort the whole transaction of the caller (a run applies many operations in one)
	if it.Status != domain.StatusSuperseded {
		var clash bool
		if err := s.q(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wa_group_memory_items WHERE tenant_id = $1 AND group_id = $2 AND fingerprint = $3 AND id <> $4 AND status <> 'superseded')`,
			it.TenantID, it.GroupID, it.Fingerprint, it.ID).Scan(&clash); err != nil {
			return err
		}
		if clash {
			return application.ErrDuplicateItem
		}
	}
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE wa_group_memory_items SET kind = $4, tier = $5, status = $6, truth_level = $7, summary = $8, actor_label = $9, evidence = $10, pinned = $11,
		  relevance = $12, last_reinforced_at = $13, superseded_by = $14, fingerprint = $15, version = version + 1, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND version = $3`,
		it.TenantID, it.ID, expectedVersion, string(it.Kind), string(it.Tier), string(it.Status), string(it.Truth), it.Summary, it.ActorLabel, evidenceJSON(it.Evidence),
		it.Pinned, float32(it.Relevance), it.LastReinforcedAt, it.SupersededBy, it.Fingerprint)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return application.ErrConflict
	}
	return nil
}

func (s *Store) ReinforceItem(ctx context.Context, tenantID, itemID uuid.UUID, relevance float64, at time.Time, evidence []domain.Evidence) error {
	var ev []byte
	if evidence != nil {
		ev = evidenceJSON(evidence)
	}
	_, err := s.q(ctx).Exec(ctx, `UPDATE wa_group_memory_items SET relevance = $3, last_reinforced_at = $4, evidence = COALESCE($5::jsonb, evidence)
		WHERE tenant_id = $1 AND id = $2 AND status <> 'superseded'`, tenantID, itemID, float32(relevance), at, ev)
	return err
}

func (s *Store) DeleteItem(ctx context.Context, tenantID, itemID uuid.UUID) error {
	tag, err := s.q(ctx).Exec(ctx, `DELETE FROM wa_group_memory_items WHERE tenant_id = $1 AND id = $2`, tenantID, itemID)
	if err == nil && tag.RowsAffected() == 0 {
		return application.ErrNotFound
	}
	return err
}

func (s *Store) EraseMemory(ctx context.Context, tenantID, groupID uuid.UUID) (int64, error) {
	tag, err := s.q(ctx).Exec(ctx, `DELETE FROM wa_group_memory_items WHERE tenant_id = $1 AND group_id = $2`, tenantID, groupID)
	if err != nil {
		return 0, err
	}
	// the reading restarts from nothing: the cursor and the counters are part of what was remembered
	// the proposals were drafted from that memory and cannot be deleted by the app (a decision is a record): their words are scrubbed
	if _, err = s.q(ctx).Exec(ctx, `UPDATE wa_group_proposals SET title = '(removido)', description = '', reject_reason = CASE WHEN reject_reason IS NULL THEN NULL ELSE '(removido)' END,
		updated_at = now() WHERE tenant_id = $1 AND group_id = $2`, tenantID, groupID); err != nil {
		return 0, err
	}
	_, err = s.q(ctx).Exec(ctx, `UPDATE wa_group_memory_state SET last_processed_at = NULL, last_processed_message_id = NULL, last_consolidated_at = NULL,
		analyze_requested_at = NULL, consolidate_requested_at = NULL, updated_at = now() WHERE tenant_id = $1 AND group_id = $2`, tenantID, groupID)
	return tag.RowsAffected(), err
}

// PurgeQuotes removes the literal excerpts from the evidence of a group's items; the refs and dates stay, so an item still says where it came from.
func (s *Store) PurgeQuotes(ctx context.Context, tenantID, groupID uuid.UUID) (int64, error) {
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE wa_group_memory_items SET evidence = COALESCE((SELECT jsonb_agg(e - 'quote') FROM jsonb_array_elements(evidence) e), '[]'::jsonb), updated_at = now()
		WHERE tenant_id = $1 AND group_id = $2 AND jsonb_array_length(evidence) > 0 AND EXISTS (SELECT 1 FROM jsonb_array_elements(evidence) e WHERE e ? 'quote')`, tenantID, groupID)
	return tag.RowsAffected(), err
}

func (s *Store) AppendEvent(ctx context.Context, ev application.ItemEvent) error {
	payload := ev.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	pj, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).Exec(ctx, `INSERT INTO wa_group_memory_item_events (tenant_id, group_id, item_id, event_type, actor_type, actor_user_id, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, ev.TenantID, ev.GroupID, ev.ItemID, string(ev.Type), ev.ActorType, ev.ActorUser, pj)
	return err
}

// ---- state and scheduling

func (s *Store) GetState(ctx context.Context, tenantID, groupID uuid.UUID) (application.State, error) {
	var st application.State
	var day *time.Time
	err := s.q(ctx).QueryRow(ctx, `SELECT last_processed_at, last_processed_message_id, last_consolidated_at, last_run_at, last_run_ok, consecutive_failures, analyze_requested_at, analyze_requested_by,
		consolidate_requested_at, prompt_version, runs, input_tokens, output_tokens, budget_day::timestamptz, budget_calls
		FROM wa_group_memory_state WHERE tenant_id = $1 AND group_id = $2`, tenantID, groupID).Scan(&st.LastProcessedAt, &st.LastProcessedMessageID, &st.LastConsolidatedAt,
		&st.LastRunAt, &st.LastRunOK, &st.ConsecutiveFailures, &st.AnalyzeRequestedAt, &st.AnalyzeRequestedBy, &st.ConsolidateRequestedAt, &st.PromptVersion, &st.Runs, &st.InputTokens, &st.OutputTokens, &day, &st.BudgetCalls)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.State{}, nil
	}
	st.BudgetDay = day
	return st, err
}

func (s *Store) RecordRun(ctx context.Context, tenantID, groupID uuid.UUID, r application.RunRecord) error {
	runs := 1
	if r.LastProcessedAt != nil { // a partial record while a run is going on; the final one counts the run
		runs = 0
	}
	var consolidatedAt *time.Time
	if r.Consolidated {
		consolidatedAt = &r.At
	}
	_, err := s.q(ctx).Exec(ctx, `
		INSERT INTO wa_group_memory_state (tenant_id, group_id, last_run_at, last_run_ok, prompt_version, runs, input_tokens, output_tokens, last_processed_at,
		  last_processed_message_id, last_consolidated_at, consecutive_failures)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, CASE WHEN $14::boolean THEN 1 ELSE 0 END)
		ON CONFLICT (tenant_id, group_id) DO UPDATE SET
		  last_run_at = EXCLUDED.last_run_at, last_run_ok = EXCLUDED.last_run_ok,
		  consecutive_failures = CASE WHEN $14::boolean THEN wa_group_memory_state.consecutive_failures + 1 WHEN $4::boolean THEN 0 ELSE wa_group_memory_state.consecutive_failures END,
		  prompt_version = CASE WHEN EXCLUDED.prompt_version <> '' THEN EXCLUDED.prompt_version ELSE wa_group_memory_state.prompt_version END,
		  runs = wa_group_memory_state.runs + EXCLUDED.runs,
		  input_tokens = wa_group_memory_state.input_tokens + EXCLUDED.input_tokens,
		  output_tokens = wa_group_memory_state.output_tokens + EXCLUDED.output_tokens,
		  last_processed_at = COALESCE(EXCLUDED.last_processed_at, wa_group_memory_state.last_processed_at),
		  last_processed_message_id = COALESCE(EXCLUDED.last_processed_message_id, wa_group_memory_state.last_processed_message_id),
		  last_consolidated_at = COALESCE(EXCLUDED.last_consolidated_at, wa_group_memory_state.last_consolidated_at),
		  analyze_requested_at = CASE WHEN $12 THEN NULL ELSE wa_group_memory_state.analyze_requested_at END,
		  consolidate_requested_at = CASE WHEN $13 THEN NULL ELSE wa_group_memory_state.consolidate_requested_at END,
		  updated_at = now()`,
		tenantID, groupID, r.At, r.OK, r.PromptVersion, runs, r.InputTokens, r.OutputTokens, r.LastProcessedAt, r.LastProcessedID, consolidatedAt, r.ClearAnalyzeAsk, r.ClearConsolidAsk, r.Failed)
	return err
}

func (s *Store) RequestAnalyze(ctx context.Context, tenantID, groupID, by uuid.UUID) error {
	_, err := s.q(ctx).Exec(ctx, `
		INSERT INTO wa_group_memory_state (tenant_id, group_id, analyze_requested_at, analyze_requested_by) VALUES ($1,$2,now(),$3)
		ON CONFLICT (tenant_id, group_id) DO UPDATE SET analyze_requested_at = now(), analyze_requested_by = $3, updated_at = now()`, tenantID, groupID, by)
	return err
}

func (s *Store) RequestConsolidate(ctx context.Context, tenantID, groupID uuid.UUID) error {
	_, err := s.q(ctx).Exec(ctx, `
		INSERT INTO wa_group_memory_state (tenant_id, group_id, consolidate_requested_at) VALUES ($1,$2,now())
		ON CONFLICT (tenant_id, group_id) DO UPDATE SET consolidate_requested_at = now(), updated_at = now()`, tenantID, groupID)
	return err
}

func (s *Store) Touch(ctx context.Context, tenantID, groupID uuid.UUID, now time.Time) error {
	_, err := s.q(ctx).Exec(ctx, `
		INSERT INTO wa_group_memory_state (tenant_id, group_id, last_checked_at) VALUES ($1,$2,$3)
		ON CONFLICT (tenant_id, group_id) DO UPDATE SET last_checked_at = $3`, tenantID, groupID, now)
	return err
}

func (s *Store) BumpBudget(ctx context.Context, tenantID, groupID uuid.UUID, day time.Time, limit int) (bool, error) {
	var calls int
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO wa_group_memory_state (tenant_id, group_id, budget_day, budget_calls) VALUES ($1,$2,$3::date,1)
		ON CONFLICT (tenant_id, group_id) DO UPDATE SET
		  budget_calls = CASE WHEN wa_group_memory_state.budget_day = EXCLUDED.budget_day THEN wa_group_memory_state.budget_calls + 1 ELSE 1 END,
		  budget_day = EXCLUDED.budget_day
		WHERE wa_group_memory_state.budget_day IS DISTINCT FROM EXCLUDED.budget_day OR wa_group_memory_state.budget_calls < $4
		RETURNING budget_calls`, tenantID, groupID, day.UTC(), limit).Scan(&calls)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// system runs fn in a system session that sees every tenant: only for the scheduler's cross-tenant listings, which read ids and nothing else.
func (s *Store) system(ctx context.Context, fn func(ctx context.Context) error) error {
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, fn)
}

func (s *Store) DueGroups(ctx context.Context, now time.Time, auto bool, limit int) ([]application.GroupRef, error) {
	autoCond := ""
	if auto {
		autoCond = ` OR (g.last_message_at IS NOT NULL AND (st.last_processed_at IS NULL OR g.last_message_at > st.last_processed_at - interval '1 minute'))`
	}
	var out []application.GroupRef
	err := s.system(ctx, func(sctx context.Context) error {
		rows, err := s.q(sctx).Query(sctx, `
			SELECT g.tenant_id, g.id FROM wa_groups g
			JOIN tenants t ON t.id = g.tenant_id AND t.status = 'active'
			LEFT JOIN wa_group_memory_state st ON st.tenant_id = g.tenant_id AND st.group_id = g.id
			WHERE g.ai_enabled AND g.enabled
			  AND (st.analyze_requested_at IS NOT NULL OR st.consolidate_requested_at IS NOT NULL`+autoCond+`
			       OR st.last_consolidated_at IS NULL OR st.last_consolidated_at < $1::timestamptz - interval '7 days')
			ORDER BY ((st.analyze_requested_at IS NOT NULL OR st.consolidate_requested_at IS NOT NULL)
			          AND (st.last_checked_at IS NULL OR GREATEST(st.analyze_requested_at, st.consolidate_requested_at) > st.last_checked_at)) DESC,
			         COALESCE(st.last_checked_at, '-infinity'::timestamptz), g.id
			LIMIT $2`, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r application.GroupRef
			if err := rows.Scan(&r.TenantID, &r.GroupID); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ---- imports

const importCols = `id, tenant_id, group_id, status, format, source_filename, sha256, size_bytes, imported_by, parser_version, msgs_total, msgs_parsed, msgs_rejected, msgs_duplicate,
	rejected_reasons, range_from, range_to, batches_total, batches_ok, batches_failed, items_created, prompt_version, extract_runs, created_at, status_changed_at,
	extracted_at, expires_at, discarded_at, discard_reason, version`

func scanImport(row pgx.Row) (domain.Import, error) {
	var im domain.Import
	var status string
	var rej []byte
	var reason *string
	if err := row.Scan(&im.ID, &im.TenantID, &im.GroupID, &status, &im.Format, &im.SourceFilename, &im.SHA256, &im.SizeBytes, &im.ImportedBy, &im.ParserVersion, &im.MsgsTotal,
		&im.MsgsParsed, &im.MsgsRejected, &im.MsgsDuplicate, &rej, &im.RangeFrom, &im.RangeTo, &im.BatchesTotal, &im.BatchesOK, &im.BatchesFailed, &im.ItemsCreated, &im.PromptVersion,
		&im.ExtractRuns, &im.CreatedAt, &im.StatusChangedAt, &im.ExtractedAt, &im.ExpiresAt, &im.DiscardedAt, &reason, &im.Version); err != nil {
		return im, err
	}
	im.Status = domain.ImportStatus(status)
	if reason != nil {
		r := domain.DiscardReason(*reason)
		im.DiscardReason = &r
	}
	im.RejectedReason = map[string]int{}
	if len(rej) > 0 {
		_ = json.Unmarshal(rej, &im.RejectedReason)
	}
	return im, nil
}

func (s *Store) InsertImport(ctx context.Context, imp domain.Import, rawEnc, normalizedEnc []byte) (domain.Import, error) {
	if imp.ID == uuid.Nil {
		imp.ID = uuid.New()
	}
	rej, _ := json.Marshal(imp.RejectedReason)
	if len(rej) == 0 || string(rej) == "null" {
		rej = []byte("{}")
	}
	// ON CONFLICT DO NOTHING instead of catching the unique violation: an error inside the request's transaction would abort it
	var inserted uuid.UUID
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO wa_group_imports (id, tenant_id, group_id, status, format, source_filename, sha256, size_bytes, imported_by, parser_version, msgs_total, msgs_parsed,
		  msgs_rejected, rejected_reasons, range_from, range_to, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (tenant_id, group_id, sha256) WHERE status <> 'discarded' DO NOTHING
		RETURNING id`,
		imp.ID, imp.TenantID, imp.GroupID, string(imp.Status), imp.Format, imp.SourceFilename, imp.SHA256, imp.SizeBytes, imp.ImportedBy, imp.ParserVersion, imp.MsgsTotal,
		imp.MsgsParsed, imp.MsgsRejected, rej, imp.RangeFrom, imp.RangeTo, imp.ExpiresAt).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return imp, application.ErrDuplicateImport
	}
	if err != nil {
		return imp, err
	}
	if _, err := s.q(ctx).Exec(ctx, `INSERT INTO wa_group_import_blobs (import_id, tenant_id, raw_enc, normalized_enc) VALUES ($1,$2,$3,$4)`, imp.ID, imp.TenantID, rawEnc, normalizedEnc); err != nil {
		return imp, err
	}
	return s.GetImport(ctx, imp.TenantID, imp.ID, false)
}

func (s *Store) GetImport(ctx context.Context, tenantID, importID uuid.UUID, forUpdate bool) (domain.Import, error) {
	q := `SELECT ` + importCols + ` FROM wa_group_imports WHERE tenant_id = $1 AND id = $2`
	if forUpdate {
		q += " FOR UPDATE"
	}
	im, err := scanImport(s.q(ctx).QueryRow(ctx, q, tenantID, importID))
	if errors.Is(err, pgx.ErrNoRows) {
		return im, application.ErrNotFound
	}
	return im, err
}

func (s *Store) ListImports(ctx context.Context, tenantID, groupID uuid.UUID, limit int) ([]domain.Import, error) {
	rows, err := s.q(ctx).Query(ctx, `SELECT `+importCols+` FROM wa_group_imports WHERE tenant_id = $1 AND group_id = $2 ORDER BY created_at DESC LIMIT $3`, tenantID, groupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Import
	for rows.Next() {
		im, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, rows.Err()
}

func (s *Store) UpdateImport(ctx context.Context, imp domain.Import) error {
	rej, _ := json.Marshal(imp.RejectedReason)
	if len(rej) == 0 || string(rej) == "null" {
		rej = []byte("{}")
	}
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE wa_group_imports SET status = $3, msgs_duplicate = $4, rejected_reasons = $5, batches_total = $6, batches_ok = $7, batches_failed = $8, items_created = $9,
		  prompt_version = $10, extract_runs = $11, extracted_at = $12,
		  status_changed_at = CASE WHEN status <> $3 THEN now() ELSE status_changed_at END, version = version + 1
		WHERE tenant_id = $1 AND id = $2 AND status <> 'discarded'`,
		imp.TenantID, imp.ID, string(imp.Status), imp.MsgsDuplicate, rej, imp.BatchesTotal, imp.BatchesOK, imp.BatchesFailed, imp.ItemsCreated, imp.PromptVersion, imp.ExtractRuns, imp.ExtractedAt)
	if err == nil && tag.RowsAffected() == 0 {
		return application.ErrConflict
	}
	return err
}

func (s *Store) ImportNormalized(ctx context.Context, tenantID, importID uuid.UUID) ([]byte, error) {
	var norm []byte
	err := s.q(ctx).QueryRow(ctx, `SELECT normalized_enc FROM wa_group_import_blobs WHERE tenant_id = $1 AND import_id = $2`, tenantID, importID).Scan(&norm)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrNotFound
	}
	return norm, err
}

func (s *Store) DiscardImport(ctx context.Context, tenantID, importID uuid.UUID, reason domain.DiscardReason, by *uuid.UUID, now time.Time) error {
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE wa_group_imports SET status = 'discarded', discarded_at = $3, discard_reason = $4, discard_confirmed_by = $5, status_changed_at = $3, version = version + 1
		WHERE tenant_id = $1 AND id = $2 AND status <> 'discarded'`, tenantID, importID, now, string(reason), by)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrAlreadyDiscarded
	}
	_, err = s.q(ctx).Exec(ctx, `DELETE FROM wa_group_import_blobs WHERE tenant_id = $1 AND import_id = $2`, tenantID, importID)
	return err
}

func (s *Store) DiscardGroupImports(ctx context.Context, tenantID, groupID uuid.UUID, reason domain.DiscardReason, now time.Time) (int64, error) {
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE wa_group_imports SET status = 'discarded', discarded_at = $3, discard_reason = $4, status_changed_at = $3, version = version + 1
		WHERE tenant_id = $1 AND group_id = $2 AND status <> 'discarded'`, tenantID, groupID, now, string(reason))
	if err != nil {
		return 0, err
	}
	_, err = s.q(ctx).Exec(ctx, `DELETE FROM wa_group_import_blobs WHERE tenant_id = $1 AND import_id IN (SELECT id FROM wa_group_imports WHERE tenant_id = $1 AND group_id = $2)`, tenantID, groupID)
	return tag.RowsAffected(), err
}

func (s *Store) importRefs(ctx context.Context, sql string, args ...any) ([]application.ImportRef, error) {
	var out []application.ImportRef
	err := s.system(ctx, func(sctx context.Context) error {
		rows, err := s.q(sctx).Query(sctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r application.ImportRef
			if err := rows.Scan(&r.TenantID, &r.GroupID, &r.ImportID); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) PendingImports(ctx context.Context, limit int) ([]application.ImportRef, error) {
	return s.importRefs(ctx, `
		SELECT i.tenant_id, i.group_id, i.id FROM wa_group_imports i
		JOIN tenants t ON t.id = i.tenant_id AND t.status = 'active'
		JOIN wa_groups g ON g.tenant_id = i.tenant_id AND g.id = i.group_id AND g.ai_enabled
		WHERE i.status = 'uploaded' OR (i.status = 'extracting' AND i.status_changed_at < now() - interval '30 minutes')
		ORDER BY i.status_changed_at, i.created_at LIMIT $1`, limit)
}

func (s *Store) ExpiredImports(ctx context.Context, now time.Time, limit int) ([]application.ImportRef, error) {
	return s.importRefs(ctx, `SELECT tenant_id, group_id, id FROM wa_group_imports WHERE status <> 'discarded' AND expires_at <= $1 ORDER BY expires_at LIMIT $2`, now, limit)
}

// ---- proposals

const proposalCols = `id, tenant_id, group_id, memory_item_id, kind, title, description, priority, due_at, proposed_by, status, decided_by, decided_at, reject_reason,
	created_case_id, created_task_id, error_code, version, created_at, updated_at`

func scanProposal(row pgx.Row) (domain.Proposal, error) {
	var p domain.Proposal
	var kind, status string
	var reject, errCode *string
	if err := row.Scan(&p.ID, &p.TenantID, &p.GroupID, &p.MemoryItemID, &kind, &p.Title, &p.Description, &p.Priority, &p.DueAt, &p.ProposedBy, &status, &p.DecidedBy, &p.DecidedAt,
		&reject, &p.CreatedCaseID, &p.CreatedTaskID, &errCode, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, err
	}
	p.Kind, p.Status = domain.ProposalKind(kind), domain.ProposalStatus(status)
	if reject != nil {
		p.RejectReason = *reject
	}
	if errCode != nil {
		p.ErrorCode = *errCode
	}
	return p, nil
}

func (s *Store) InsertProposal(ctx context.Context, p domain.Proposal) (domain.Proposal, bool, error) {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	var id uuid.UUID
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO wa_group_proposals (id, tenant_id, group_id, memory_item_id, kind, title, description, priority, due_at, proposed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (tenant_id, memory_item_id, kind) WHERE memory_item_id IS NOT NULL AND status IN ('pending_approval','created') DO NOTHING
		RETURNING id`, p.ID, p.TenantID, p.GroupID, p.MemoryItemID, string(p.Kind), p.Title, p.Description, p.Priority, p.DueAt, p.ProposedBy).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gerr := scanProposal(s.q(ctx).QueryRow(ctx, `SELECT `+proposalCols+` FROM wa_group_proposals
			WHERE tenant_id = $1 AND memory_item_id = $2 AND kind = $3 AND status IN ('pending_approval','created')`, p.TenantID, p.MemoryItemID, string(p.Kind)))
		return existing, false, gerr
	}
	if err != nil {
		return p, false, err
	}
	stored, err := s.GetProposal(ctx, p.TenantID, id, false)
	return stored, true, err
}

func (s *Store) GetProposal(ctx context.Context, tenantID, proposalID uuid.UUID, forUpdate bool) (domain.Proposal, error) {
	q := `SELECT ` + proposalCols + ` FROM wa_group_proposals WHERE tenant_id = $1 AND id = $2`
	if forUpdate {
		q += " FOR UPDATE"
	}
	p, err := scanProposal(s.q(ctx).QueryRow(ctx, q, tenantID, proposalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, application.ErrNotFound
	}
	return p, err
}

func (s *Store) LiveProposal(ctx context.Context, tenantID, itemID uuid.UUID, kind domain.ProposalKind) (domain.Proposal, bool, error) {
	p, err := scanProposal(s.q(ctx).QueryRow(ctx, `SELECT `+proposalCols+` FROM wa_group_proposals
		WHERE tenant_id = $1 AND memory_item_id = $2 AND kind = $3 AND status IN ('pending_approval','created')`, tenantID, itemID, string(kind)))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

func (s *Store) ListProposals(ctx context.Context, tenantID, groupID uuid.UUID, status string, limit int) ([]domain.Proposal, error) {
	args := []any{tenantID, groupID, limit}
	where := "tenant_id = $1 AND group_id = $2"
	if status != "" {
		args = append(args, status)
		where += " AND status = $4"
	}
	rows, err := s.q(ctx).Query(ctx, `SELECT `+proposalCols+` FROM wa_group_proposals WHERE `+where+` ORDER BY created_at DESC LIMIT $3`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProposal(ctx context.Context, p domain.Proposal, expectedVersion int) error {
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE wa_group_proposals SET title = $4, description = $5, priority = $6, due_at = $7, status = $8, decided_by = $9, decided_at = $10, reject_reason = $11,
		  created_case_id = $12, created_task_id = $13, error_code = NULLIF($14, ''), version = version + 1, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND version = $3`, p.TenantID, p.ID, expectedVersion, p.Title, p.Description, p.Priority, p.DueAt, string(p.Status), p.DecidedBy, p.DecidedAt,
		nullIfEmpty(p.RejectReason), p.CreatedCaseID, p.CreatedTaskID, p.ErrorCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return application.ErrConflict
	}
	return nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
