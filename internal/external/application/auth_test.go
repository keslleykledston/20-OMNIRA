package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/application"
	"github.com/omnira/omnira/internal/external/domain"
)

type fakeLookup struct {
	rec   *application.CredentialRecord
	err   error
	calls int
	last  string
}

func (f *fakeLookup) Lookup(_ context.Context, keyID string) (*application.CredentialRecord, error) {
	f.calls++
	f.last = keyID
	return f.rec, f.err
}

func record(t *testing.T) (string, *application.CredentialRecord) {
	t.Helper()
	token, keyID, digest, err := domain.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	_ = keyID
	return token, &application.CredentialRecord{
		CredentialID: uuid.New(), InstallationID: uuid.New(), TenantID: uuid.New(), Digest: digest,
		Scopes: []string{"conversation.read", "message.read"}, RatePerMin: 600, Name: "crm",
	}
}

func TestAuthenticateResolvesTheTenantFromTheInstallationOnly(t *testing.T) {
	token, rec := record(t)
	a := application.NewAuthenticator(&fakeLookup{rec: rec})
	p, err := a.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if p.TenantID != rec.TenantID || p.InstallationID != rec.InstallationID {
		t.Fatalf("principal = %+v, want tenant %s installation %s", p, rec.TenantID, rec.InstallationID)
	}
	if !p.Has(domain.ScopeConversationRead) || p.Has(domain.ScopeMessageSend) {
		t.Fatalf("scopes = %v", p.Scopes)
	}
}

func TestAuthenticateRefusesEverythingElseWithTheSameError(t *testing.T) {
	token, rec := record(t)
	other, _ := record(t)
	wrongSecret := token[:len(token)-1] + map[bool]string{true: "B", false: "A"}[token[len(token)-1] == 'A']
	badScope := *rec
	badScope.Scopes = []string{"conversation.read", "root.everything"}
	cases := map[string]struct {
		bearer string
		look   *fakeLookup
	}{
		"empty":            {"", &fakeLookup{rec: rec}},
		"no prefix":        {strings.TrimPrefix(token, domain.TokenPrefix), &fakeLookup{rec: rec}},
		"truncated":        {token[:len(token)-3], &fakeLookup{rec: rec}},
		"unknown key":      {token, &fakeLookup{rec: nil}},
		"wrong secret":     {wrongSecret, &fakeLookup{rec: rec}},
		"other token":      {other, &fakeLookup{rec: rec}},
		"unknown scope":    {token, &fakeLookup{rec: &badScope}},
		"junk characters":  {domain.TokenPrefix + strings.Repeat("!", 60), &fakeLookup{rec: rec}},
		"query injection":  {token + "' OR '1'='1", &fakeLookup{rec: rec}},
		"trailing newline": {token + "\n", &fakeLookup{rec: rec}},
	}
	for name, c := range cases {
		_, err := application.NewAuthenticator(c.look).Authenticate(context.Background(), c.bearer)
		if !errors.Is(err, application.ErrUnauthenticated) {
			t.Errorf("%s: err = %v, want ErrUnauthenticated", name, err)
		}
	}
}

func TestAuthenticateDoesNotTouchTheDatabaseForMalformedTokens(t *testing.T) {
	f := &fakeLookup{}
	a := application.NewAuthenticator(f)
	for _, b := range []string{"", "Bearer x", "omn_ext_", "omn_ext_zzzzzzzzzzzzzzzz.short", strings.Repeat("a", 5000)} {
		_, _ = a.Authenticate(context.Background(), b)
	}
	if f.calls != 0 {
		t.Fatalf("lookup called %d times for malformed input", f.calls)
	}
}

func TestInfrastructureFailureIsNotAnAuthenticationFailure(t *testing.T) {
	token, _ := record(t)
	boom := errors.New("db down")
	_, err := application.NewAuthenticator(&fakeLookup{err: boom}).Authenticate(context.Background(), token)
	if !errors.Is(err, boom) || errors.Is(err, application.ErrUnauthenticated) {
		t.Fatalf("err = %v, want the infrastructure error (503), not a 401", err)
	}
}

func TestTokenShapeAndDigest(t *testing.T) {
	token, keyID, digest, err := domain.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	k, secret, ok := domain.ParseToken(token)
	if !ok || k != keyID || len(keyID) != 16 {
		t.Fatalf("parse = %q %q %v", k, secret, ok)
	}
	if !domain.SecretMatches(secret, digest) || domain.SecretMatches(secret+"x", digest) {
		t.Fatal("digest comparison is wrong")
	}
	if len(digest) != 32 {
		t.Fatalf("digest length %d", len(digest))
	}
	other, _, _, _ := domain.NewToken()
	if other == token {
		t.Fatal("two tokens are equal")
	}
}

func TestParseScopesRejectsUnknownAndDeduplicates(t *testing.T) {
	got, err := domain.ParseScopes([]string{"message.read", "conversation.read", "message.read"})
	if err != nil || len(got) != 2 || got[0] != domain.ScopeConversationRead {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range [][]string{nil, {}, {"conversation.read", "admin"}, {""}} {
		if _, err := domain.ParseScopes(bad); err == nil {
			t.Errorf("ParseScopes(%v) accepted", bad)
		}
	}
}

func TestLimiterBurstRefillAndRetryAfter(t *testing.T) {
	l := application.NewLimiter()
	now := time.Unix(1000, 0)
	application.SetNow(l, func() time.Time { return now })
	allowed := 0
	for i := 0; i < 100; i++ {
		if ok, _ := l.Allow("inst", 60); ok { // 60/min: burst 10
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("burst allowed %d, want 10", allowed)
	}
	if ok, wait := l.Allow("inst", 60); ok || wait < time.Second {
		t.Fatalf("exhausted bucket: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow("other", 60); !ok {
		t.Fatal("buckets must be independent per key")
	}
	now = now.Add(3 * time.Second) // 1 token per second
	got := 0
	for i := 0; i < 10; i++ {
		if ok, _ := l.Allow("inst", 60); ok {
			got++
		}
	}
	if got != 3 {
		t.Fatalf("after 3s refilled %d, want 3", got)
	}
}
