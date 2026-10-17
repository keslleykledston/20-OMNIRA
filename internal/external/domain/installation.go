package domain

import (
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Status of an installation. A revoked installation never comes back; suspended can be reactivated.
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusRevoked   Status = "revoked"
)

const (
	NameMaxRunes         = 80
	DefaultRatePerMin    = 600
	MinRatePerMin        = 10
	MaxRatePerMin        = 6000
	MinLeaseSeconds      = 60
	MaxLeaseSeconds      = 86400
	MaxLiveCredentials   = 2 // the current one and, during a rotation, the next
	DefaultCredentialTTL = 365 * 24 * time.Hour
	MaxCredentialTTL     = 2 * 365 * 24 * time.Hour
	MinCredentialTTL     = time.Hour
)

var (
	ErrInvalidName         = errors.New("name must have 1 to 80 characters")
	ErrInvalidRate         = errors.New("rate_limit_per_min must be between 10 and 6000")
	ErrInvalidLease        = errors.New("lease_seconds must be between 60 and 86400")
	ErrInvalidTTL          = errors.New("credential lifetime must be between 1 hour and 730 days")
	ErrTooManyCredentials  = errors.New("an installation holds at most 2 live credentials")
	ErrInstallationRevoked = errors.New("installation is revoked")
	ErrNotFound            = errors.New("not found")
	ErrNameTaken           = errors.New("an installation with this name already exists")
)

// Installation is one application approved by a tenant. It is NOT a user, a membership or a Hub agent.
type Installation struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	Name              string
	Status            Status
	Scopes            []Scope
	RatePerMin        int
	LeaseSeconds      *int
	AllowOutsideHours bool
	QueueIDs          []uuid.UUID // empty: every queue of the tenant
	ChannelIDs        []uuid.UUID // empty: every line of the tenant
	CreatedBy         *uuid.UUID
	CreatedAt         time.Time
	UpdatedAt         time.Time
	SuspendedAt       *time.Time
	RevokedAt         *time.Time
}

// Credential is the public record of a secret. The secret itself exists only in the response that issued it.
type Credential struct {
	ID             uuid.UUID
	InstallationID uuid.UUID
	KeyID          string
	Label          string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RevokedAt      *time.Time
	LastUsedAt     *time.Time
}

func (c Credential) Live(now time.Time) bool { return c.RevokedAt == nil && c.ExpiresAt.After(now) }

func ValidName(raw string) (string, error) {
	name := trimSpace(raw)
	if n := utf8.RuneCountInString(name); n < 1 || n > NameMaxRunes {
		return "", ErrInvalidName
	}
	return name, nil
}

func ValidRate(n int) error {
	if n < MinRatePerMin || n > MaxRatePerMin {
		return ErrInvalidRate
	}
	return nil
}

func ValidLease(n *int) error {
	if n != nil && (*n < MinLeaseSeconds || *n > MaxLeaseSeconds) {
		return ErrInvalidLease
	}
	return nil
}

func ValidTTL(d time.Duration) error {
	if d < MinCredentialTTL || d > MaxCredentialTTL {
		return ErrInvalidTTL
	}
	return nil
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// Principal is the authenticated external caller. It carries ONLY what the credential proved: which installation, therefore which tenant.
// It never carries a user id, a membership or a system-admin flag: an application is not a person.
type Principal struct {
	CredentialID      uuid.UUID
	InstallationID    uuid.UUID
	TenantID          uuid.UUID
	Name              string
	Scopes            []Scope
	RatePerMin        int
	LeaseSeconds      *int
	AllowOutsideHours bool
}

func (p *Principal) Has(s Scope) bool {
	if p == nil {
		return false
	}
	for _, k := range p.Scopes {
		if k == s {
			return true
		}
	}
	return false
}
