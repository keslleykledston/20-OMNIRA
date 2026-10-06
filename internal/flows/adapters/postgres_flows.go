package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PostgresFlowRepository persists flows and versions. Every query filters by the TenantContext tenant AND relies on RLS;
// a tenant id is never accepted from a caller-provided value.
type PostgresFlowRepository struct {
	pool *pgxpool.Pool
}

var _ ports.FlowRepository = (*PostgresFlowRepository)(nil)

func NewPostgresFlowRepository(pool *pgxpool.Pool) *PostgresFlowRepository {
	return &PostgresFlowRepository{pool: pool}
}

func tenantOf(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("flows: tenant context required")
	}
	return tc.TenantID, nil
}

func (r *PostgresFlowRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

// inTx runs fn on the caller's transaction when there is one (the normal request/worker path) or on a private one.
func (r *PostgresFlowRepository) inTx(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	if tx, ok := r.q(ctx).(pgx.Tx); ok {
		return fn(ctx, tx)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const flowColumns = `id, tenant_id, slug, name, description, flow_type, status, draft_definition, draft_revision, active_version_id,
	priority, is_default, trigger_filter, restart_policy, source_template_slug, source_template_version, template_installed_at,
	created_by, created_at, updated_at, archived_at`

func scanFlow(row pgx.Row) (*domain.Flow, error) {
	var f domain.Flow
	var filter []byte
	err := row.Scan(&f.ID, &f.TenantID, &f.Slug, &f.Name, &f.Description, &f.Type, &f.Status, &f.DraftDefinition, &f.DraftRevision,
		&f.ActiveVersionID, &f.Priority, &f.IsDefault, &filter, &f.RestartPolicy, &f.SourceTemplateSlug, &f.SourceTemplateVersion,
		&f.TemplateInstalledAt, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt, &f.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(filter) > 0 {
		if err := json.Unmarshal(filter, &f.TriggerFilter); err != nil {
			return nil, fmt.Errorf("flows: corrupt trigger_filter: %w", err)
		}
	}
	return &f, nil
}

const versionColumns = `id, tenant_id, flow_id, version, definition, definition_hash, note, published_by, published_at`

func scanVersion(row pgx.Row) (*domain.FlowVersion, error) {
	var v domain.FlowVersion
	err := row.Scan(&v.ID, &v.TenantID, &v.FlowID, &v.Version, &v.Definition, &v.DefinitionHash, &v.Note, &v.PublishedBy, &v.PublishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNoSuchVersion
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func mapWriteErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "flows_tenant_id_slug_key":
			return domain.ErrSlugTaken
		case "flows_one_default_per_type_uq":
			return fmt.Errorf("%w: another default flow of this type already exists", domain.ErrInvalid)
		}
	}
	return err
}

func (r *PostgresFlowRepository) CreateFlow(ctx context.Context, f *domain.Flow) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if f.TenantID != tenantID {
		return errors.New("flows: flow tenant differs from the session tenant")
	}
	filter, err := json.Marshal(f.TriggerFilter)
	if err != nil {
		return err
	}
	_, err = r.q(ctx).Exec(ctx, `
		INSERT INTO flows (id, tenant_id, slug, name, description, flow_type, status, draft_definition, draft_revision, priority,
		  is_default, trigger_filter, restart_policy, source_template_slug, source_template_version, template_installed_at,
		  created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		f.ID, f.TenantID, f.Slug, f.Name, f.Description, f.Type, f.Status, []byte(f.DraftDefinition), f.DraftRevision, f.Priority,
		f.IsDefault, filter, f.RestartPolicy, f.SourceTemplateSlug, f.SourceTemplateVersion, f.TemplateInstalledAt,
		f.CreatedBy, f.CreatedAt, f.UpdatedAt)
	return mapWriteErr(err)
}

func (r *PostgresFlowRepository) GetFlow(ctx context.Context, id uuid.UUID) (*domain.Flow, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return scanFlow(r.q(ctx).QueryRow(ctx, `SELECT `+flowColumns+` FROM flows WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (r *PostgresFlowRepository) GetFlowBySlug(ctx context.Context, slug string) (*domain.Flow, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return scanFlow(r.q(ctx).QueryRow(ctx, `SELECT `+flowColumns+` FROM flows WHERE tenant_id=$1 AND slug=$2`, tenantID, slug))
}

func (r *PostgresFlowRepository) ListFlows(ctx context.Context, f ports.ListFilter) ([]*domain.Flow, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT `+flowColumns+` FROM flows
		WHERE tenant_id=$1 AND ($2 = '' OR status=$2) AND ($3 = '' OR flow_type=$3)
		ORDER BY updated_at DESC, id DESC LIMIT $4 OFFSET $5`, tenantID, string(f.Status), string(f.Type), f.Limit, f.Offset)
	if err != nil {
		return nil, fmt.Errorf("flows: list: %w", err)
	}
	defer rows.Close()
	out := []*domain.Flow{}
	for rows.Next() {
		fl, err := scanFlow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fl)
	}
	return out, rows.Err()
}

// missingOrConflict explains why a guarded UPDATE touched no row.
func (r *PostgresFlowRepository) missingOrConflict(ctx context.Context, tenantID, id uuid.UUID) error {
	var status string
	err := r.q(ctx).QueryRow(ctx, `SELECT status FROM flows WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == string(domain.FlowStatusArchived) {
		return domain.ErrArchived
	}
	return domain.ErrRevisionConflict
}

func (r *PostgresFlowRepository) SaveDraft(ctx context.Context, id uuid.UUID, expectedRevision int, name, description string, definition json.RawMessage) (*domain.Flow, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if len(definition) > domain.MaxDefinitionBytes {
		return nil, fmt.Errorf("%w: definition exceeds %d bytes", domain.ErrInvalid, domain.MaxDefinitionBytes)
	}
	f, err := scanFlow(r.q(ctx).QueryRow(ctx, `
		UPDATE flows SET name=$4, description=$5, draft_definition=$6, draft_revision=draft_revision+1, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND draft_revision=$3 AND status <> 'archived'
		RETURNING `+flowColumns, tenantID, id, expectedRevision, name, description, []byte(definition)))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, r.missingOrConflict(ctx, tenantID, id)
	}
	return f, err
}

func (r *PostgresFlowRepository) UpdateSettings(ctx context.Context, id uuid.UUID, s ports.Settings) (*domain.Flow, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	filter, err := json.Marshal(s.TriggerFilter)
	if err != nil {
		return nil, err
	}
	f, err := scanFlow(r.q(ctx).QueryRow(ctx, `
		UPDATE flows SET priority=$3, is_default=$4, trigger_filter=$5, restart_policy=$6, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status <> 'archived'
		RETURNING `+flowColumns, tenantID, id, s.Priority, s.IsDefault, filter, s.RestartPolicy))
	if errors.Is(err, domain.ErrNotFound) {
		if e := r.missingOrConflict(ctx, tenantID, id); !errors.Is(e, domain.ErrRevisionConflict) {
			return nil, e
		}
		return nil, domain.ErrNotFound
	}
	return f, mapWriteErr(err)
}

func (r *PostgresFlowRepository) Publish(ctx context.Context, id uuid.UUID, expectedRevision int, note string, by *uuid.UUID) (*domain.FlowVersion, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out *domain.FlowVersion
	err = r.inTx(ctx, func(ctx context.Context, q platformdb.Querier) error {
		var revision int
		var status string
		// Row lock: concurrent publishes of one flow are serialized, so version numbers never collide.
		if err := q.QueryRow(ctx, `SELECT draft_revision, status FROM flows WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, id).Scan(&revision, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		if status == string(domain.FlowStatusArchived) {
			return domain.ErrArchived
		}
		if revision != expectedRevision {
			return domain.ErrRevisionConflict
		}
		// Idempotent publish: an unchanged draft (double click, retry) re-activates the latest version instead of
		// creating an identical one.
		var draftHash string
		if err := q.QueryRow(ctx, `SELECT encode(sha256(convert_to(draft_definition::text, 'UTF8')), 'hex') FROM flows WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&draftHash); err != nil {
			return err
		}
		if latest, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+` FROM flow_versions WHERE tenant_id=$1 AND flow_id=$2 ORDER BY version DESC LIMIT 1`, tenantID, id)); err == nil && latest.DefinitionHash == draftHash {
			if _, err := q.Exec(ctx, `UPDATE flows SET status='published', active_version_id=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id, latest.ID); err != nil {
				return err
			}
			out = latest
			return nil
		} else if err != nil && !errors.Is(err, domain.ErrNoSuchVersion) {
			return err
		}
		v, err := scanVersion(q.QueryRow(ctx, `
			INSERT INTO flow_versions (tenant_id, flow_id, version, definition, definition_hash, note, published_by)
			SELECT f.tenant_id, f.id,
			       COALESCE((SELECT max(version) FROM flow_versions WHERE flow_id=f.id), 0) + 1,
			       f.draft_definition,
			       encode(sha256(convert_to(f.draft_definition::text, 'UTF8')), 'hex'),
			       $3, $4
			FROM flows f WHERE f.tenant_id=$1 AND f.id=$2
			RETURNING `+versionColumns, tenantID, id, note, by))
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE flows SET status='published', active_version_id=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id, v.ID); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

func (r *PostgresFlowRepository) ActivateVersion(ctx context.Context, flowID uuid.UUID, version int) (*domain.Flow, *domain.FlowVersion, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, nil, err
	}
	var flow *domain.Flow
	var ver *domain.FlowVersion
	err = r.inTx(ctx, func(ctx context.Context, q platformdb.Querier) error {
		var status string
		if err := q.QueryRow(ctx, `SELECT status FROM flows WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, flowID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		if status == string(domain.FlowStatusArchived) {
			return domain.ErrArchived
		}
		v, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+` FROM flow_versions WHERE tenant_id=$1 AND flow_id=$2 AND version=$3`, tenantID, flowID, version))
		if err != nil {
			return err
		}
		f, err := scanFlow(q.QueryRow(ctx, `UPDATE flows SET status='published', active_version_id=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2 RETURNING `+flowColumns, tenantID, flowID, v.ID))
		if err != nil {
			return err
		}
		flow, ver = f, v
		return nil
	})
	return flow, ver, err
}

func (r *PostgresFlowRepository) Archive(ctx context.Context, id uuid.UUID) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE flows SET status='archived', is_default=false, archived_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status <> 'archived'`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return r.missingOrArchivedNoop(ctx, tenantID, id)
	}
	return nil
}

// missingOrArchivedNoop: archiving an already archived flow is idempotent; a missing one is an error.
func (r *PostgresFlowRepository) missingOrArchivedNoop(ctx context.Context, tenantID, id uuid.UUID) error {
	var n int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM flows WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *PostgresFlowRepository) GetVersion(ctx context.Context, id uuid.UUID) (*domain.FlowVersion, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return scanVersion(r.q(ctx).QueryRow(ctx, `SELECT `+versionColumns+` FROM flow_versions WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (r *PostgresFlowRepository) GetVersionByNumber(ctx context.Context, flowID uuid.UUID, version int) (*domain.FlowVersion, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return scanVersion(r.q(ctx).QueryRow(ctx, `SELECT `+versionColumns+` FROM flow_versions WHERE tenant_id=$1 AND flow_id=$2 AND version=$3`, tenantID, flowID, version))
}

func (r *PostgresFlowRepository) ListVersions(ctx context.Context, flowID uuid.UUID, limit, offset int) ([]*domain.FlowVersion, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+versionColumns+` FROM flow_versions WHERE tenant_id=$1 AND flow_id=$2 ORDER BY version DESC LIMIT $3 OFFSET $4`, tenantID, flowID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("flows: list versions: %w", err)
	}
	defer rows.Close()
	out := []*domain.FlowVersion{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *PostgresFlowRepository) RecordPackInstallation(ctx context.Context, p *domain.PackInstallation) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if p.TenantID != tenantID {
		return errors.New("flows: installation tenant differs from the session tenant")
	}
	sel, _ := json.Marshal(p.SelectedTemplates)
	maps, _ := json.Marshal(p.Mappings)
	_, err = r.q(ctx).Exec(ctx, `INSERT INTO flow_pack_installations (id, tenant_id, pack_slug, pack_version, selected_templates, mappings, installed_by, installed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, p.ID, p.TenantID, p.PackSlug, p.PackVersion, sel, maps, p.InstalledBy, p.InstalledAt)
	return err
}

func (r *PostgresFlowRepository) RecordTemplateInstallation(ctx context.Context, t *domain.TemplateInstallation) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if t.TenantID != tenantID {
		return errors.New("flows: installation tenant differs from the session tenant")
	}
	maps, _ := json.Marshal(t.Mappings)
	_, err = r.q(ctx).Exec(ctx, `INSERT INTO flow_template_installations (id, tenant_id, template_slug, template_version, flow_id, pack_installation_id, mappings, installed_by, installed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, t.ID, t.TenantID, t.TemplateSlug, t.TemplateVersion, t.FlowID, t.PackInstallationID, maps, t.InstalledBy, t.InstalledAt)
	return err
}

func (r *PostgresFlowRepository) ListTemplateInstallations(ctx context.Context, flowID uuid.UUID) ([]*domain.TemplateInstallation, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT id, tenant_id, template_slug, template_version, flow_id, pack_installation_id, mappings, installed_by, installed_at
		FROM flow_template_installations WHERE tenant_id=$1 AND flow_id=$2 ORDER BY installed_at DESC`, tenantID, flowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.TemplateInstallation{}
	for rows.Next() {
		var t domain.TemplateInstallation
		var maps []byte
		if err := rows.Scan(&t.ID, &t.TenantID, &t.TemplateSlug, &t.TemplateVersion, &t.FlowID, &t.PackInstallationID, &maps, &t.InstalledBy, &t.InstalledAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(maps, &t.Mappings)
		out = append(out, &t)
	}
	return out, rows.Err()
}
