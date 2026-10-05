// Package domain models the verified channel identities of INTERNAL users (ADR-0018). An identity says "this phone /
// e-mail / provider participant is staff member X". It is a security boundary: only a verified identity makes inbound
// traffic internal, and it NEVER grants a permission (RBAC is the only authority).
package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Type string

const (
	TypePhone               Type = "phone"
	TypeEmail               Type = "email"
	TypeProviderParticipant Type = "provider_participant"
)

func (t Type) Valid() bool { return t == TypePhone || t == TypeEmail || t == TypeProviderParticipant }

type Status string

const (
	StatusPending  Status = "pending"
	StatusVerified Status = "verified"
	StatusRevoked  Status = "revoked"
)

// VerificationSource is how an identity was verified. There is deliberately no AI source: an AI can suggest, never verify.
type VerificationSource string

const (
	SourceAdmin            VerificationSource = "admin"
	SourceProviderVerified VerificationSource = "provider_verified"
	SourceChallenge        VerificationSource = "challenge"
	SourceImportVerified   VerificationSource = "import_verified"
)

func (s VerificationSource) Valid() bool {
	return s == SourceAdmin || s == SourceProviderVerified || s == SourceChallenge || s == SourceImportVerified
}

type Resolution string

const (
	ResolutionConfirmedInternal Resolution = "confirmed_internal"
	ResolutionIdentityRevoked   Resolution = "identity_revoked"
)

func (r Resolution) Valid() bool {
	return r == ResolutionConfirmedInternal || r == ResolutionIdentityRevoked
}

var (
	ErrInvalidIdentity   = errors.New("identity: invalid identity value")
	ErrNotFound          = errors.New("identity: not found")
	ErrUserNotInTenant   = errors.New("identity: the user is not a member of this tenant")
	ErrDuplicate         = errors.New("identity: this user already has that identity")
	ErrAlreadyVerified   = errors.New("identity: that identity is already verified for another user")
	ErrInvalidState      = errors.New("identity: the identity is not in a state that allows this")
	ErrSourceNotAllowed  = errors.New("identity: verification source not allowed here")
	ErrConflictNotFound  = errors.New("identity: conflict not found")
	ErrConflictNotOpen   = errors.New("identity: conflict already resolved")
	ErrInvalidResolution = errors.New("identity: invalid resolution")
)

var (
	e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
)

// NormalizePhone returns the E.164 form of a phone typed by a human or received from a provider. It never guesses a
// country: a number without a country code is refused. "00" is read as "+"; spaces, dots, dashes and parentheses go.
func NormalizePhone(raw string) (string, error) {
	var b strings.Builder
	for i, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '.' || r == '-' || r == '(' || r == ')':
		default:
			return "", ErrInvalidIdentity
		}
	}
	s := b.String()
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	if !strings.HasPrefix(s, "+") {
		// provider ids (WhatsApp) are digits with the country code but no "+": accept only when long enough to carry one
		if len(s) < 10 {
			return "", ErrInvalidIdentity
		}
		s = "+" + s
	}
	if !e164.MatchString(s) {
		return "", ErrInvalidIdentity
	}
	return s, nil
}

// NormalizeEmail lowercases and trims; it validates only the shape (one "@", non-empty parts, no spaces).
func NormalizeEmail(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	at := strings.IndexByte(s, '@')
	if at < 1 || at != strings.LastIndexByte(s, '@') || at == len(s)-1 || strings.ContainsAny(s, " \t\r\n<>,;") || utf8.RuneCountInString(s) > 254 || !strings.Contains(s[at+1:], ".") {
		return "", ErrInvalidIdentity
	}
	return s, nil
}

// NormalizeParticipant normalizes a provider participant id (JID/LID); its meaning is scoped to one provider connection.
func NormalizeParticipant(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || utf8.RuneCountInString(s) > 200 || strings.ContainsAny(s, " \t\r\n") {
		return "", ErrInvalidIdentity
	}
	return s, nil
}

// ParticipantScope is the scope string of a provider participant identity.
func ParticipantScope(provider string, connectionID uuid.UUID) string {
	return strings.ToLower(strings.TrimSpace(provider)) + ":" + connectionID.String()
}

type Identity struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	UserID             uuid.UUID
	Type               Type
	Scope              string
	Raw                string
	Normalized         string
	Status             Status
	VerificationSource *VerificationSource
	VerifiedAt         *time.Time
	RevokedAt          *time.Time
	CreatedAt          time.Time
}

type Conflict struct {
	ID         uuid.UUID
	IdentityID uuid.UUID
	ContactID  uuid.UUID
	Status     string
	Resolution *Resolution
	Note       *string
	DetectedAt time.Time
	ResolvedAt *time.Time
}

// Match is what the inbound resolver needs: the verified internal owner of an identity and whether a contact conflict
// is still open for it (open = the resolver must NOT treat the sender as a customer nor as safely internal).
type Match struct {
	IdentityID        uuid.UUID
	UserID            uuid.UUID
	HasOpenConflict   bool
	ConflictConfirmed bool
}
