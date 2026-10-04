package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// Handoff lets a person invite a customer to continue a subject in a private chat (ADR-0017 Wave 8). The invitation is an
// opaque, short-lived, single-use token: the server keeps only its hash and never logs it.
type HandoffStatus string

const (
	HandoffPending  HandoffStatus = "pending"
	HandoffRedeemed HandoffStatus = "redeemed"
	HandoffRevoked  HandoffStatus = "revoked"
)

const (
	HandoffTokenPrefix = "omn-"
	DefaultHandoffTTL  = 24 * time.Hour
	MaxHandoffTTL      = 72 * time.Hour
	MaxPendingHandoffs = 3 // per topic
)

var (
	ErrHandoffInvalid   = errors.New("intelligence: handoff token is not valid")
	ErrTooManyHandoffs  = errors.New("intelligence: the topic already has the maximum of pending handoffs")
	handoffTokenPattern = regexp.MustCompile(`omn-[A-Za-z0-9_-]{43}`)
)

type TopicHandoff struct {
	ID                     uuid.UUID
	TenantID               uuid.UUID
	TopicThreadID          uuid.UUID
	SourceGroupID          *uuid.UUID
	Status                 HandoffStatus
	ExpiresAt              time.Time
	CreatedByUserID        *uuid.UUID
	CreatedAt              time.Time
	RedeemedAt             *time.Time
	RedeemedConversationID *uuid.UUID
	RevokedAt              *time.Time
}

// EffectiveStatus is what a person sees: a pending invitation past its time is expired.
func (h TopicHandoff) EffectiveStatus(now time.Time) string {
	if h.Status == HandoffPending && !now.Before(h.ExpiresAt) {
		return "expired"
	}
	return string(h.Status)
}

// NewHandoffToken returns a fresh token (256 random bits) and its hash. The token is shown once.
func NewHandoffToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = HandoffTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashHandoffToken(token), nil
}

func HashHandoffToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// FindHandoffToken extracts a token-shaped string from a customer message (the customer may add words around it).
// It only looks at the SHAPE; whether the token is real is decided by the store.
func FindHandoffToken(text string) (string, bool) {
	if len(text) < len(HandoffTokenPrefix)+43 {
		return "", false
	}
	m := handoffTokenPattern.FindString(text)
	return m, m != ""
}

// ClampHandoffTTL applies the default and the ceiling.
func ClampHandoffTTL(requested time.Duration) time.Duration {
	switch {
	case requested <= 0:
		return DefaultHandoffTTL
	case requested > MaxHandoffTTL:
		return MaxHandoffTTL
	}
	return requested
}
