package sessionctx

import (
	"context"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

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
		pid    uint32
		at     int64
		want   string
	}{
		{uuid.MustParse("11111111-2222-3333-4444-555555555555"), false, 4242, 1700000000, "b1|11111111-2222-3333-4444-555555555555|0|4242|1700000000|86fdee3b8d7069cdef53351e1519718a1e39cc8d0bef733ef6f5e863db077617"},
		{uuid.Nil, true, 7, 1700000001, "b1|00000000-0000-0000-0000-000000000000|1|7|1700000001|59c31b05e42b94f8c989ad2a233e2455e559e800532c4a6db27153bbe3d6c01f"},
	}
	for _, c := range cases {
		if got := Sign(key, c.user, c.system, c.pid, time.Unix(c.at, 0)); got != c.want {
			t.Errorf("Sign(%s,%v,%d,%d)\n got %s\nwant %s", c.user, c.system, c.pid, c.at, got, c.want)
		}
	}
}

func TestAnotherKeyOrAnotherFieldGivesAnotherSignature(t *testing.T) {
	k1 := []byte(strings.Repeat("a", 32))
	k2 := []byte(strings.Repeat("b", 32))
	u := uuid.New()
	at := time.Unix(1700000000, 0)
	base := Sign(k1, u, false, 1, at)
	for name, other := range map[string]string{
		"key":    Sign(k2, u, false, 1, at),
		"system": Sign(k1, u, true, 1, at),
		"user":   Sign(k1, uuid.New(), false, 1, at),
		"time":   Sign(k1, u, false, 1, at.Add(time.Second)),
		"pid":    Sign(k1, u, false, 2, at),
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

func (r fakeRow) Scan(dest ...any) error {
	*(dest[0].(*bool)) = r.ok
	return nil
}

type fakeQuerier struct {
	ok    bool
	sql   []string
	calls int
}

func (f *fakeQuerier) PID() uint32 { return 1234 }

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
		t.Error("ctx_begin was called without a key (it does not exist before migration 000118)")
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
