package sessionctx

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Vectors computed independently (Python hmac/hashlib) so the format the database verifies cannot drift unnoticed.
func TestSignMatchesTheIndependentVectors(t *testing.T) {
	key, _ := hex.DecodeString("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	cases := []struct {
		user   uuid.UUID
		system bool
		at     int64
		want   string
	}{
		{uuid.MustParse("11111111-2222-3333-4444-555555555555"), false, 1700000000, "b1|11111111-2222-3333-4444-555555555555|0|1700000000|2cbe1be1723fc1294c6a1f020315509bf0707a1017ab0f49e5601314f759688f"},
		{uuid.Nil, true, 1700000001, "b1||1|1700000001|cd276cd7050a7c9ae052841d4e006dfe04d14335647462a861a4816c8cca0b6a"},
	}
	for _, c := range cases {
		if got := Sign(key, c.user, c.system, time.Unix(c.at, 0)); got != c.want {
			t.Errorf("Sign(%s,%v,%d)\n got %s\nwant %s", c.user, c.system, c.at, got, c.want)
		}
	}
}

func TestAnotherKeyOrAnotherFieldGivesAnotherSignature(t *testing.T) {
	k1 := []byte(strings.Repeat("a", 32))
	k2 := []byte(strings.Repeat("b", 32))
	u := uuid.New()
	at := time.Unix(1700000000, 0)
	base := Sign(k1, u, false, at)
	for name, other := range map[string]string{
		"key":    Sign(k2, u, false, at),
		"system": Sign(k1, u, true, at),
		"user":   Sign(k1, uuid.New(), false, at),
		"time":   Sign(k1, u, false, at.Add(time.Second)),
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
