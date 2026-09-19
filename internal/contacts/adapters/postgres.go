package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	contactdomain "github.com/omnira/omnira/internal/contacts/domain"
	"github.com/omnira/omnira/internal/contacts/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type PostgresContactRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresContactRepository(pool *pgxpool.Pool) ports.ContactRepository {
	return &PostgresContactRepository{pool: pool}
}

func (r *PostgresContactRepository) Store(ctx context.Context, contact *contactdomain.Contact) error {
	if err := validateTenantContact(ctx, contact); err != nil {
		return err
	}
	_, err := platformdb.QuerierFromContext(ctx, r.pool).Exec(ctx, `
		INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		contact.ID, contact.TenantID, contact.DisplayName, contact.PhoneE164, contact.Email, contact.Status, contact.CreatedAt, contact.UpdatedAt)
	if err != nil {
		return fmt.Errorf("contact: store: %w", err)
	}
	return nil
}

func (r *PostgresContactRepository) FindByID(ctx context.Context, id uuid.UUID) (*contactdomain.Contact, error) {
	if err := requireTenantContext(ctx); err != nil {
		return nil, err
	}
	return scanContact(platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, contactQuery+` WHERE tenant_id = $1 AND id = $2`, tenantID(ctx), id))
}

func (r *PostgresContactRepository) FindByPhone(ctx context.Context, phoneE164 string) (*contactdomain.Contact, error) {
	if err := requireTenantContext(ctx); err != nil {
		return nil, err
	}
	return scanContact(platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, contactQuery+` WHERE tenant_id = $1 AND phone_e164 = $2`, tenantID(ctx), phoneE164))
}

func (r *PostgresContactRepository) UpsertByPhone(ctx context.Context, contact *contactdomain.Contact) (*contactdomain.Contact, error) {
	if err := validateTenantContact(ctx, contact); err != nil {
		return nil, err
	}
	return scanContact(platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		INSERT INTO contacts (tenant_id, display_name, phone_e164, email, status)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, phone_e164) DO UPDATE SET
			display_name = CASE WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name ELSE contacts.display_name END,
			updated_at = now()
		RETURNING id, tenant_id, display_name, phone_e164, email, status, created_at, updated_at`,
		contact.TenantID, contact.DisplayName, contact.PhoneE164, contact.Email, contact.Status))
}

func (r *PostgresContactRepository) List(ctx context.Context, limit, offset int) ([]*contactdomain.Contact, error) {
	if err := requireTenantContext(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := platformdb.QuerierFromContext(ctx, r.pool).Query(ctx, contactQuery+` WHERE tenant_id = $1 ORDER BY updated_at DESC, id DESC LIMIT $2 OFFSET $3`, tenantID(ctx), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("contact: list: %w", err)
	}
	defer rows.Close()
	var contacts []*contactdomain.Contact
	for rows.Next() {
		contact, scanErr := scanContact(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		contacts = append(contacts, contact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("contact: list rows: %w", err)
	}
	return contacts, nil
}

func (r *PostgresContactRepository) Update(ctx context.Context, contact *contactdomain.Contact) error {
	if err := validateTenantContact(ctx, contact); err != nil {
		return err
	}
	result, err := platformdb.QuerierFromContext(ctx, r.pool).Exec(ctx, `
		UPDATE contacts SET display_name = $3, email = $4, status = $5, updated_at = $6
		WHERE id = $1 AND tenant_id = $2`, contact.ID, contact.TenantID, contact.DisplayName, contact.Email, contact.Status, contact.UpdatedAt)
	if err != nil {
		return fmt.Errorf("contact: update: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("contact: update: %w", pgx.ErrNoRows)
	}
	return nil
}

const contactQuery = `SELECT id, tenant_id, display_name, phone_e164, email, status, created_at, updated_at FROM contacts`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanContact(row rowScanner) (*contactdomain.Contact, error) {
	contact := &contactdomain.Contact{}
	if err := row.Scan(&contact.ID, &contact.TenantID, &contact.DisplayName, &contact.PhoneE164, &contact.Email, &contact.Status, &contact.CreatedAt, &contact.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("contact: scan: %w", err)
	}
	return contact, nil
}

func requireTenantContext(ctx context.Context) error {
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return fmt.Errorf("contact: tenant context required: %w", err)
	}
	return nil
}

func tenantID(ctx context.Context) uuid.UUID {
	tc, _ := tenancydomain.FromContext(ctx)
	return tc.TenantID
}

func validateTenantContact(ctx context.Context, contact *contactdomain.Contact) error {
	if contact == nil || contact.ID == uuid.Nil {
		return errors.New("contact: valid contact is required")
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return fmt.Errorf("contact: tenant context required: %w", err)
	}
	if contact.TenantID != tc.TenantID {
		return errors.New("contact: tenant context mismatch")
	}
	return nil
}
