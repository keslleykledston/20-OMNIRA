package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/platform/authn"
	ticketsports "github.com/omnira/omnira/internal/tickets/ports"
)

// PRODUCT.7B1A: proves GET /api/v1/tenants/{tenant_id}/crm/companies is
// genuinely tenant-scoped through the SAME PRODUCT.6-L runtime resolver
// CreateExternalTicket already uses, never the removed global h.k3gClient
// (the confirmed PRODUCT.7B P0). Every test resolves through a fake that
// records which tenantID it was asked for, so a regression back to a
// shared/global client would fail here even if a single-tenant response
// happened to look correct.

// perTenantResolver hands back a distinct ports.CompanyDirectory per
// tenant and fails with ResolutionNoConfiguration for any tenant not in
// the map — modelling exactly what K3GTicketingRuntimeResolver.Resolve
// does for a tenant with no configured K3G connection, without a real
// Postgres/credential store.
type perTenantResolver struct {
	byTenant map[uuid.UUID]ticketsports.CompanyDirectory
	seen     []uuid.UUID
}

func (r *perTenantResolver) Resolve(ctx context.Context, tenantID uuid.UUID) (*ticketsports.TicketingRuntime, error) {
	r.seen = append(r.seen, tenantID)
	dir, ok := r.byTenant[tenantID]
	if !ok {
		return nil, &ticketsports.ResolutionError{Code: ticketsports.ResolutionNoConfiguration, Message: "no K3G connection configured for this tenant"}
	}
	return &ticketsports.TicketingRuntime{CompanyDirectory: dir}, nil
}

type staticCompanies struct {
	items []ticketsports.Company
	err   error
}

func (s *staticCompanies) ListCompanies(ctx context.Context) ([]ticketsports.Company, error) {
	return s.items, s.err
}

func listCompaniesMux(h *CRMHandlers) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/crm/companies", h.ListCompanies)
	return mux
}

func listCompaniesRequest(tenantID, actorID uuid.UUID) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenantID.String()+"/crm/companies", nil)
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: actorID})
	return req.WithContext(ctx)
}

// A/B/C/D/G. Two tenants, two distinct directories: each request returns
// ONLY its own tenant's companies, and the resolver itself was asked for
// the matching tenant id — proves the CLIENT/DIRECTORY is tenant-scoped,
// not merely the JSON filtered after the fact.
func TestListCompaniesHTTPTenantScoped(t *testing.T) {
	tenantA, tenantB := uuid.New(), uuid.New()
	resolver := &perTenantResolver{byTenant: map[uuid.UUID]ticketsports.CompanyDirectory{
		tenantA: &staticCompanies{items: []ticketsports.Company{{ExternalID: "a-1", Name: "Empresa A", CNPJ: "11.111.111/0001-11", Active: true}}},
		tenantB: &staticCompanies{items: []ticketsports.Company{{ExternalID: "b-1", Name: "Empresa B", CNPJ: "22.222.222/0001-22", Active: true}}},
	}}
	handler := NewCRMHandlers(nil)
	handler.SetCompanyDirectoryResolver(resolver)
	mux := listCompaniesMux(handler)

	recA := httptest.NewRecorder()
	mux.ServeHTTP(recA, listCompaniesRequest(tenantA, uuid.New()))
	if recA.Code != http.StatusOK {
		t.Fatalf("tenant A status = %d, want 200: %s", recA.Code, recA.Body.String())
	}
	if !strings.Contains(recA.Body.String(), "Empresa A") || strings.Contains(recA.Body.String(), "Empresa B") {
		t.Fatalf("tenant A must see ONLY its own directory: %s", recA.Body.String())
	}

	recB := httptest.NewRecorder()
	mux.ServeHTTP(recB, listCompaniesRequest(tenantB, uuid.New()))
	if recB.Code != http.StatusOK {
		t.Fatalf("tenant B status = %d, want 200: %s", recB.Code, recB.Body.String())
	}
	if !strings.Contains(recB.Body.String(), "Empresa B") || strings.Contains(recB.Body.String(), "Empresa A") {
		t.Fatalf("tenant B must see ONLY its own directory: %s", recB.Body.String())
	}

	if len(resolver.seen) != 2 || resolver.seen[0] != tenantA || resolver.seen[1] != tenantB {
		t.Fatalf("resolver must be called with the exact per-request tenant id, got %v want [%s %s]", resolver.seen, tenantA, tenantB)
	}
}

// E/F. a tenant with no K3G configuration fails closed (503, the same body
// ticketingUnavailable already uses elsewhere) and never falls back to
// another tenant's directory.
func TestListCompaniesHTTPNoConfigurationFailsClosed(t *testing.T) {
	tenantA := uuid.New()
	tenantWithoutConfig := uuid.New()
	resolver := &perTenantResolver{byTenant: map[uuid.UUID]ticketsports.CompanyDirectory{
		tenantA: &staticCompanies{items: []ticketsports.Company{{ExternalID: "a-1", Name: "Empresa A", Active: true}}},
	}}
	handler := NewCRMHandlers(nil)
	handler.SetCompanyDirectoryResolver(resolver)
	mux := listCompaniesMux(handler)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, listCompaniesRequest(tenantWithoutConfig, uuid.New()))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Empresa A") {
		t.Fatalf("must never fall back to another tenant's directory: %s", rec.Body.String())
	}
}

// F (explicit). ListCompanies never depends on h.k3gClient: a handler with
// the resolver wired but k3gClient left at its zero value (nil) must still
// serve real, tenant-scoped data.
func TestListCompaniesHTTPNeverUsesGlobalK3GClient(t *testing.T) {
	tenantA := uuid.New()
	resolver := &perTenantResolver{byTenant: map[uuid.UUID]ticketsports.CompanyDirectory{
		tenantA: &staticCompanies{items: []ticketsports.Company{{ExternalID: "a-1", Name: "Empresa A", Active: true}}},
	}}
	handler := NewCRMHandlers(nil) // h.k3gClient is nil — never set
	handler.SetCompanyDirectoryResolver(resolver)
	mux := listCompaniesMux(handler)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, listCompaniesRequest(tenantA, uuid.New()))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Empresa A") {
		t.Fatalf("must resolve via the tenant-scoped resolver alone, got %d: %s", rec.Code, rec.Body.String())
	}
}

// I. unauthenticated request is denied before any resolver call.
func TestListCompaniesHTTPUnauthenticatedDenied(t *testing.T) {
	tenantA := uuid.New()
	resolver := &perTenantResolver{byTenant: map[uuid.UUID]ticketsports.CompanyDirectory{}}
	handler := NewCRMHandlers(nil)
	handler.SetCompanyDirectoryResolver(resolver)
	mux := listCompaniesMux(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenantA.String()+"/crm/companies", nil)
	// no authn.Principal injected into context
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if len(resolver.seen) != 0 {
		t.Fatalf("resolver must not be called for an unauthenticated request, got %v", resolver.seen)
	}
}

// H. the handler resolves strictly from the path tenant_id (the only
// identity tenantSession authorizes). This GET accepts no body and no
// query parameter that could name a different tenant or provider
// configuration — a client-supplied query string is simply ignored.
func TestListCompaniesHTTPIgnoresClientSuppliedProviderOverride(t *testing.T) {
	tenantA, tenantB := uuid.New(), uuid.New()
	resolver := &perTenantResolver{byTenant: map[uuid.UUID]ticketsports.CompanyDirectory{
		tenantA: &staticCompanies{items: []ticketsports.Company{{ExternalID: "a-1", Name: "Empresa A", Active: true}}},
		tenantB: &staticCompanies{items: []ticketsports.Company{{ExternalID: "b-1", Name: "Empresa B", Active: true}}},
	}}
	handler := NewCRMHandlers(nil)
	handler.SetCompanyDirectoryResolver(resolver)
	mux := listCompaniesMux(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenantA.String()+"/crm/companies?tenant_id="+tenantB.String()+"&company_id=b-1", nil)
	ctx := authn.WithPrincipal(req.Context(), &authn.Principal{UserID: uuid.New()})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Empresa A") || strings.Contains(rec.Body.String(), "Empresa B") {
		t.Fatalf("a query string must never override the path tenant, got %d: %s", rec.Code, rec.Body.String())
	}
}

// K. a live provider error (after successful tenant resolution) maps to a
// safe generic 500 — never a raw provider error string reaching the client.
func TestListCompaniesHTTPProviderErrorMapsToSafeGeneric500(t *testing.T) {
	tenantA := uuid.New()
	resolver := &perTenantResolver{byTenant: map[uuid.UUID]ticketsports.CompanyDirectory{
		tenantA: &staticCompanies{err: errors.New("k3g: secret credential leak detail xyz")},
	}}
	handler := NewCRMHandlers(nil)
	handler.SetCompanyDirectoryResolver(resolver)
	mux := listCompaniesMux(handler)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, listCompaniesRequest(tenantA, uuid.New()))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret credential leak detail") {
		t.Fatalf("provider error detail must never reach the HTTP response: %s", rec.Body.String())
	}
}

// Resolver not wired (misconfigured deployment) fails closed exactly like
// the pre-existing ticketingUnavailable convention — PRODUCT.7B1A
// deliberately removes the old "return empty list" behavior for an unwired
// handler, since a silent empty 200 is indistinguishable from "this tenant
// really has zero companies."
func TestListCompaniesHTTPResolverNotWiredFailsClosed(t *testing.T) {
	handler := NewCRMHandlers(nil)
	mux := listCompaniesMux(handler)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, listCompaniesRequest(uuid.New(), uuid.New()))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// Only companies the CRM reports as active are listed; inactive ones are neither
// shown nor counted, and a tenant whose companies are all inactive gets [] (not null).
func TestListCompaniesHTTPOnlyActive(t *testing.T) {
	tenantA, tenantAllInactive := uuid.New(), uuid.New()
	resolver := &perTenantResolver{byTenant: map[uuid.UUID]ticketsports.CompanyDirectory{
		tenantA: &staticCompanies{items: []ticketsports.Company{
			{ExternalID: "a-1", Name: "Ativa Um", Active: true},
			{ExternalID: "a-2", Name: "Inativa Dois", Active: false},
			{ExternalID: "a-3", Name: "Ativa Tres", Active: true},
		}},
		tenantAllInactive: &staticCompanies{items: []ticketsports.Company{{ExternalID: "z-1", Name: "Toda Inativa", Active: false}}},
	}}
	handler := NewCRMHandlers(nil)
	handler.SetCompanyDirectoryResolver(resolver)
	mux := listCompaniesMux(handler)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, listCompaniesRequest(tenantA, uuid.New()))
	var got CompanyListResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(got.Items) != 2 || got.Items[0].ID != "a-1" || got.Items[1].ID != "a-3" || strings.Contains(rec.Body.String(), "Inativa Dois") {
		t.Fatalf("only active companies, in CRM order: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, listCompaniesRequest(tenantAllInactive, uuid.New()))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Fatalf("all-inactive tenant must get an empty list, got %d: %s", rec.Code, rec.Body.String())
	}
}
