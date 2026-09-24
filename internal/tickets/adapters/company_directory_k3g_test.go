package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/omnira/omnira/internal/tool/connectors"
)

// K3GCompanyDirectory is a thin field-mapping adapter over
// K3GCRMClient.ListCompanies (already covered by
// internal/tool/connectors' own HTTP-level tests). The only logic this
// package adds is the ID->ExternalID / IsActive->Active mapping and error
// passthrough — small enough to be worth one direct unit test rather than
// left unverified, but not worth a live/network test.
type fakeK3GCompanyLister struct {
	companies []connectors.CRMCompany
	err       error
}

func (f *fakeK3GCompanyLister) ListCompanies(ctx context.Context) ([]connectors.CRMCompany, error) {
	return f.companies, f.err
}

func TestK3GCompanyDirectoryMapsFields(t *testing.T) {
	d := NewK3GCompanyDirectory(&fakeK3GCompanyLister{companies: []connectors.CRMCompany{
		{ID: "d38e7970-635d-490b-a119-749ee6f1fe23", Name: "ACME_TESTE", IsActive: true},
		{ID: "inactive-co", Name: "Inactive Co", IsActive: false},
	}})
	got, err := d.ListCompanies(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].ExternalID != "d38e7970-635d-490b-a119-749ee6f1fe23" || !got[0].Active || got[1].Active {
		t.Fatalf("unexpected mapping: %+v", got)
	}
}

func TestK3GCompanyDirectoryPropagatesError(t *testing.T) {
	boom := errors.New("crm unreachable")
	d := NewK3GCompanyDirectory(&fakeK3GCompanyLister{err: boom})
	_, err := d.ListCompanies(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
