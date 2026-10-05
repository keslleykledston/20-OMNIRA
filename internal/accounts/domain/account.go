// Package domain holds the customer-account model (ADR-0018): the local, stable identity of an organization the tenant
// serves, and its links to provider companies. A CustomerAccount is NOT a Tenant and NOT a person.
package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type AccountType string

const (
	TypeCustomer AccountType = "customer"
	TypePartner  AccountType = "partner"
	// TypeInternal is an internal ORGANIZATION account. Staff are Users, never accounts or contacts.
	TypeInternal AccountType = "internal"
	TypeOther    AccountType = "other"
)

func (t AccountType) Valid() bool {
	return t == TypeCustomer || t == TypePartner || t == TypeInternal || t == TypeOther
}

type AccountStatus string

const (
	StatusActive   AccountStatus = "active"
	StatusInactive AccountStatus = "inactive"
	StatusArchived AccountStatus = "archived"
)

func (s AccountStatus) Valid() bool {
	return s == StatusActive || s == StatusInactive || s == StatusArchived
}

const MaxNameRunes = 200

var (
	ErrAccountNotFound   = errors.New("accounts: account not found")
	ErrInvalidAccount    = errors.New("accounts: invalid account")
	ErrExternalLinkTaken = errors.New("accounts: that provider company is already linked to another account")
)

type Account struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Name       string
	Type       AccountType
	Status     AccountStatus
	Metadata   []byte // JSON object
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
}

// NormalizeName trims and bounds a name; it never invents one.
func NormalizeName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || utf8.RuneCountInString(n) > MaxNameRunes {
		return "", ErrInvalidAccount
	}
	return n, nil
}

type LinkSource string

const (
	SourceDirectorySelection LinkSource = "directory_selection"
	SourceTicketFlow         LinkSource = "ticket_flow"
	SourceCRMEvidence        LinkSource = "crm_evidence"
	SourceImport             LinkSource = "import"
)

func (s LinkSource) Valid() bool {
	return s == SourceDirectorySelection || s == SourceTicketFlow || s == SourceCRMEvidence || s == SourceImport
}

type LinkStatus string

const (
	LinkActive   LinkStatus = "active"
	LinkInactive LinkStatus = "inactive"
)

// ExternalLink is the identity of an account in ONE provider connection. The external id is meaningful only together
// with the provider and the connection.
type ExternalLink struct {
	ID                   uuid.UUID
	TenantID             uuid.UUID
	AccountID            uuid.UUID
	Provider             string
	ConnectionID         uuid.UUID
	ExternalCompanyID    string
	ExternalNameSnapshot *string
	Status               LinkStatus
	Source               LinkSource
	VerifiedAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (l ExternalLink) Validate() error {
	if l.TenantID == uuid.Nil || l.AccountID == uuid.Nil || l.ConnectionID == uuid.Nil ||
		strings.TrimSpace(l.Provider) == "" || strings.TrimSpace(l.ExternalCompanyID) == "" || !l.Source.Valid() {
		return ErrInvalidAccount
	}
	return nil
}
