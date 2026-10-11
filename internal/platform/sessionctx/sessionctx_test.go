package sessionctx

import (
	"context"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Vectors computed independently (Python hmac/hashlib) so the format the database verifies cannot drift unnoticed.
func TestSignMatchesTheIndependentVectors(t *testing.T) {
	key, _ := hex.DecodeString("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	cases := []struct {
		user   uuid.UUID
		system bool
		nonce  string
		want   string
	}{
		{uuid.MustParse("11111111-2222-3333-4444-555555555555"), false, "00112233445566778899aabbccddeeff", "b1|11111111-2222-3333-4444-555555555555|0|00112233445566778899aabbccddeeff|601f8693cd910a41eadca19592e30f6805091e2297ab017114edc92087c852b0"},
		{uuid.Nil, true, "ffeeddccbbaa99887766554433221100", "b1|00000000-0000-0000-0000-000000000000|1|ffeeddccbbaa99887766554433221100|82176c9ea4120baee4334ee94e469fb6e634ed4d489eeb0f7e547fa8254e8f30"},
	}
	for _, c := range cases {
		if got := Sign(key, c.user, c.system, c.nonce); got != c.want {
			t.Errorf("Sign(%s,%v,%s)\n got %s\nwant %s", c.user, c.system, c.nonce, got, c.want)
		}
	}
}

func TestAnotherKeyOrAnotherFieldGivesAnotherSignature(t *testing.T) {
	k1 := []byte(strings.Repeat("a", 32))
	k2 := []byte(strings.Repeat("b", 32))
	u := uuid.New()
	n := "00112233445566778899aabbccddeeff"
	base := Sign(k1, u, false, n)
	for name, other := range map[string]string{
		"key":    Sign(k2, u, false, n),
		"system": Sign(k1, u, true, n),
		"user":   Sign(k1, uuid.New(), false, n),
		"nonce":  Sign(k1, u, false, "ffffffffffffffffffffffffffffffff"),
	} {
		if other == base {
			t.Errorf("changing the %s did not change the signed context", name)
		}
	}
}

func TestParseKey(t *testing.T) {
	good := strings.Repeat("ab", 32)
	if k, err := parseSessionKey(good); err != nil || len(k) != 32 {
		t.Errorf("a valid key: %v %v", k, err)
	}
	if k, err := parseSessionKey("  " + good + "\n"); err != nil || len(k) != 32 {
		t.Errorf("surrounding whitespace must be tolerated: %v %v", k, err)
	}
	if k, err := parseSessionKey(""); err != nil || k != nil {
		t.Errorf("no key is not an error: %v %v", k, err)
	}
	for name, raw := range map[string]string{
		"not hex":   strings.Repeat("zz", 32),
		"too short": strings.Repeat("ab", 31),
		"odd":       good + "a",
	} {
		if _, err := parseSessionKey(raw); err == nil {
			t.Errorf("%s: accepted (a bad key must be loud, never a silent downgrade to unsigned sessions)", name)
		}
	}
}

type fakeRow struct{ ok bool }

// Scan answers like the two statements Set runs: the challenge (one string) and the answer (a bool and the two settings).
func (r fakeRow) Scan(dest ...any) error {
	if len(dest) == 1 {
		*(dest[0].(*string)) = "00112233445566778899aabbccddeeff"
		return nil
	}
	*(dest[0].(*bool)) = r.ok
	return nil
}

type fakeQuerier struct {
	ok    bool
	sql   []string
	calls int
}

func (f *fakeQuerier) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.sql = append(f.sql, sql)
	return pgconn.CommandTag{}, nil
}

func (f *fakeQuerier) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.calls++
	f.sql = append(f.sql, sql)
	return fakeRow{ok: f.ok}
}

func TestARefusedContextIsCountedAndAnAcceptedOneIsNot(t *testing.T) {
	key := strings.Repeat("ab", 32)
	t.Setenv(KeyEnv, key)
	keyOnce, keyVal, keyErr = sync.Once{}, nil, nil // re-read the environment
	defer func() { keyOnce, keyVal, keyErr = sync.Once{}, nil, nil }()

	before := Rejections()
	if err := Set(context.Background(), &fakeQuerier{ok: true}, uuid.New(), false); err != nil {
		t.Fatal(err)
	}
	if Rejections() != before {
		t.Error("an accepted context was counted as refused")
	}
	if err := Set(context.Background(), &fakeQuerier{ok: false}, uuid.New(), false); err != nil {
		t.Fatalf("a refused context must not fail the request by itself (the database mode decides what it means): %v", err)
	}
	if Rejections() != before+1 {
		t.Errorf("a refused context was not counted: %d -> %d", before, Rejections())
	}
}

func TestWithoutAKeyOnlyTheLegacySettingsAreSetAndNothingIsAskedOfTheDatabase(t *testing.T) {
	t.Setenv(KeyEnv, "")
	keyOnce, keyVal, keyErr = sync.Once{}, nil, nil
	defer func() { keyOnce, keyVal, keyErr = sync.Once{}, nil, nil }()
	f := &fakeQuerier{}
	if err := Set(context.Background(), f, uuid.New(), true); err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Error("the database was asked for a challenge without a key (ctx_challenge does not exist before migration 000118)")
	}
	if len(f.sql) != 2 || !strings.Contains(f.sql[0], "app.current_user_id") || !strings.Contains(f.sql[1], "app.is_system_admin") {
		t.Errorf("legacy settings not set: %v", f.sql)
	}
}

func TestABadKeyInTheEnvironmentFailsEverySessionLoudly(t *testing.T) {
	t.Setenv(KeyEnv, "nothex")
	keyOnce, keyVal, keyErr = sync.Once{}, nil, nil
	defer func() { keyOnce, keyVal, keyErr = sync.Once{}, nil, nil }()
	if err := Set(context.Background(), &fakeQuerier{ok: true}, uuid.New(), false); err == nil {
		t.Error("a misconfigured key must fail the session, not silently downgrade it to unsigned")
	}
}
