package adapters

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/accounts/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
)

// TicketAccountResolver implements ports.AccountResolver on top of account_external_links (ADR-0018): the account that
// represents a provider company on one connection, created with its link the first time the company is used. It runs
// inside the caller's tenant session. The company comes from the tenant's CompanyDirectory (validated and ACTIVE), never
// from the browser, so a name/status here is the directory's own.
type TicketAccountResolver struct{ repo *PostgresRepository }

var _ ports.AccountResolver = (*TicketAccountResolver)(nil)

func NewTicketAccountResolver(pool *pgxpool.Pool) *TicketAccountResolver {
	return &TicketAccountResolver{repo: NewPostgresRepository(pool)}
}

func (r *TicketAccountResolver) ResolveForCompany(ctx context.Context, tenantID, connectionID uuid.UUID, company ports.Company) (uuid.UUID, error) {
	ext := strings.TrimSpace(company.ExternalID)
	if tenantID == uuid.Nil || connectionID == uuid.Nil || ext == "" {
		return uuid.Nil, domain.ErrInvalidAccount
	}
	if tc, err := tenancydomain.FromContext(ctx); err == nil && tc.Source == tenancydomain.AccessSourceHubServe {
		// a Hub agent attending the instance: no direct insert on accounts or links; the database function does the same find-or-create, for ticket.create
		return r.repo.MaterializeDelegatedCompanyAccount(ctx, tenantID, connectionID, ext, company.Name, domain.SourceTicketFlow)
	}
	existing, err := r.repo.FindByExternal(ctx, tenantID, domain.ProviderK3G, connectionID, ext)
	if err != nil {
		return uuid.Nil, err
	}
	if existing != nil {
		// the directory just said the company is active: the link follows it
		if existing.Status != domain.LinkActive {
			if err := r.repo.SetLinkStatus(ctx, tenantID, existing.ID, domain.LinkActive); err != nil {
				return uuid.Nil, err
			}
		}
		return existing.AccountID, nil
	}
	name := strings.TrimSpace(company.Name)
	if name == "" {
		name = "Empresa " + ext
	}
	acc, err := r.repo.CreateAccount(ctx, tenantID, name, domain.TypeCustomer)
	if err != nil {
		return uuid.Nil, err
	}
	now := time.Now().UTC()
	link, err := r.repo.UpsertExternalLink(ctx, domain.ExternalLink{
		TenantID: tenantID, AccountID: acc.ID, Provider: domain.ProviderK3G, ConnectionID: connectionID, ExternalCompanyID: ext,
		ExternalNameSnapshot: &name, Source: domain.SourceTicketFlow, VerifiedAt: &now,
	})
	if errors.Is(err, domain.ErrExternalLinkTaken) {
		// another request linked the company first: keep theirs, retire the orphan we just made
		archived := domain.StatusArchived
		_, _ = r.repo.UpdateAccount(ctx, tenantID, acc.ID, nil, nil, &archived)
		if won, ferr := r.repo.FindByExternal(ctx, tenantID, domain.ProviderK3G, connectionID, ext); ferr == nil && won != nil {
			return won.AccountID, nil
		}
	}
	if err != nil {
		return uuid.Nil, err
	}
	return link.AccountID, nil
}
