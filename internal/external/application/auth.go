// Package application holds the rules of the external integration that need no SQL and no HTTP: authenticating a bearer token against a
// stored digest, and rate limiting per installation.
package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/domain"
)

// ErrUnauthenticated is the ONLY error Authenticate returns for a bad token. It never says which part was wrong (unknown key, wrong secret,
// expired, revoked, suspended), so a caller cannot probe which credentials exist.
var ErrUnauthenticated = errors.New("unauthenticated")

// CredentialRecord is what the lookup returns for a live credential of an active installation of an active tenant.
type CredentialRecord struct {
	CredentialID      uuid.UUID
	InstallationID    uuid.UUID
	TenantID          uuid.UUID
	Digest            []byte
	Scopes            []string
	RatePerMin        int
	LeaseSeconds      *int
	AllowOutsideHours bool
	Name              string
}

// CredentialLookup finds a credential by its public key id. (nil, nil) means "none that is usable".
type CredentialLookup interface {
	Lookup(ctx context.Context, keyID string) (*CredentialRecord, error)
}

// dummyDigest is compared when the key is unknown, so the work done does not depend on whether the key exists.
var dummyDigest = domain.DigestSecret("omnira-external-dummy-secret")

// Authenticator turns a bearer token into a Principal. The tenant of the Principal is the tenant of the installation the credential
// belongs to: it is never read from the request.
type Authenticator struct {
	lookup CredentialLookup
}

func NewAuthenticator(l CredentialLookup) *Authenticator { return &Authenticator{lookup: l} }

func (a *Authenticator) Authenticate(ctx context.Context, bearer string) (*domain.Principal, error) {
	keyID, secret, ok := domain.ParseToken(bearer)
	if !ok {
		return nil, ErrUnauthenticated
	}
	rec, err := a.lookup.Lookup(ctx, keyID)
	if err != nil {
		return nil, err // infrastructure failure: the caller answers 503, not 401
	}
	if rec == nil {
		_ = domain.SecretMatches(secret, dummyDigest)
		return nil, ErrUnauthenticated
	}
	if !domain.SecretMatches(secret, rec.Digest) {
		return nil, ErrUnauthenticated
	}
	scopes, err := domain.ParseScopes(rec.Scopes)
	if err != nil {
		return nil, ErrUnauthenticated // a stored scope this build does not know: fail closed
	}
	return &domain.Principal{
		CredentialID:      rec.CredentialID,
		InstallationID:    rec.InstallationID,
		TenantID:          rec.TenantID,
		Name:              rec.Name,
		Scopes:            scopes,
		RatePerMin:        rec.RatePerMin,
		LeaseSeconds:      rec.LeaseSeconds,
		AllowOutsideHours: rec.AllowOutsideHours,
	}, nil
}
