package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// TenantStatus — estado de um tenant.
type TenantStatus string

const (
	TenantStatusActive    TenantStatus = "active"
	TenantStatusInactive  TenantStatus = "inactive"
	TenantStatusSuspended TenantStatus = "suspended"
)

// IsolationProfile — política de isolamento de dados.
type IsolationProfile string

const (
	IsolationSharedStrong  IsolationProfile = "shared_strong_isolation"
	IsolationDedicated     IsolationProfile = "dedicated_database"
)

// Tenant — entidade de domínio representando uma organização.
type Tenant struct {
	ID                uuid.UUID
	LegalName         string
	TradeName         *string
	TaxID             *string
	IsolationProfile  IsolationProfile
	Status            TenantStatus
	CreatedAt         time.Time
	UpdatedAt      time.Time
}

// NewTenant — factory com validação básica.
func NewTenant(legalName string, profile IsolationProfile) (*Tenant, error) {
	if legalName == "" {
		return nil, errors.New("legal_name is required")
	}

	return &Tenant{
		ID:               uuid.New(),
		LegalName:        legalName,
		IsolationProfile: profile,
		Status:           TenantStatusActive,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}, nil
}

// Deactivate — muda status pra inactive.
func (t *Tenant) Deactivate() error {
	if t.Status == TenantStatusSuspended {
		return errors.New("cannot deactivate a suspended tenant")
	}
	t.Status = TenantStatusInactive
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// Suspend — muda status pra suspended (operação administrativa).
func (t *Tenant) Suspend() error {
	t.Status = TenantStatusSuspended
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// IsActive — verdade se status == active.
func (t *Tenant) IsActive() bool {
	return t.Status == TenantStatusActive
}
