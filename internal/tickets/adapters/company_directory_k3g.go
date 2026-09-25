package adapters

import (
	"context"

	"github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// k3gCompanyLister is the subset of K3GCRMClient this adapter needs —
// declared locally so this package does not have to import the concrete
// *connectors.K3GCRMClient type, only its ListCompanies behavior.
type k3gCompanyLister interface {
	ListCompanies(ctx context.Context) ([]connectors.CRMCompany, error)
}

// K3GCompanyDirectory adapts K3GCRMClient.ListCompanies (PRODUCT.6-K0's
// trusted company source) to ports.CompanyDirectory. It never invents
// activity or identity: a company is exactly what the tenant's real K3G
// CRM reports.
type K3GCompanyDirectory struct{ client k3gCompanyLister }

func NewK3GCompanyDirectory(client k3gCompanyLister) *K3GCompanyDirectory {
	return &K3GCompanyDirectory{client: client}
}

func (d *K3GCompanyDirectory) ListCompanies(ctx context.Context) ([]ports.Company, error) {
	companies, err := d.client.ListCompanies(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ports.Company, 0, len(companies))
	for _, c := range companies {
		out = append(out, ports.Company{ExternalID: c.ID, Name: c.Name, CNPJ: c.CNPJ, Active: c.IsActive})
	}
	return out, nil
}
