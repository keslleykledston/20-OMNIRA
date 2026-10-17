package delivery_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/external/delivery"
)

// Vectors computed with an independent implementation (Python hmac): the signed bytes are "<timestamp>.<raw body>".
func TestSignatureMatchesIndependentVectors(t *testing.T) {
	cases := []struct {
		secret string
		ts     int64
		body   string
		want   string
	}{
		{"0123456789abcdef0123456789abcdef", 1760000000, `{"event_id":"e1"}`, "v1=33b51f6c4fb3004d4280b349ce11cae0b171f4a15c93f0980b00fc2280e683c4"},
		{"s", 1, ``, "v1=7845f55296e832a0ffeb7b3aa8d5dc4dc995a2d0de16b76379cff0f81f072e1a"},
	}
	for _, c := range cases {
		if got := delivery.Signature([]byte(c.secret), c.ts, []byte(c.body)); got != c.want {
			t.Errorf("Signature(%q,%d,%q) = %s, want %s", c.secret, c.ts, c.body, got, c.want)
		}
	}
}

func TestVerifyRejectsTamperingReplayAndTheWrongSecret(t *testing.T) {
	now := time.Unix(1760000000, 0)
	secret, other := []byte("current-secret-current-secret-00"), []byte("old-secret-old-secret-old-secret")
	body := []byte(`{"event_id":"e1","type":"message.created.v1"}`)
	ts := now.Unix()
	h := delivery.SignatureHeader(secret, nil, ts, body)
	tsh := strconv.FormatInt(ts, 10)
	if !delivery.Verify(secret, h, tsh, body, now) {
		t.Fatal("a genuine call must verify")
	}
	for name, ok := range map[string]bool{
		"tampered body":    delivery.Verify(secret, h, tsh, append(append([]byte{}, body...), ' '), now),
		"wrong secret":     delivery.Verify(other, h, tsh, body, now),
		"other timestamp":  delivery.Verify(secret, h, strconv.FormatInt(ts+1, 10), body, now),
		"replayed late":    delivery.Verify(secret, h, tsh, body, now.Add(delivery.MaxClockSkew+time.Second)),
		"from the future":  delivery.Verify(secret, h, tsh, body, now.Add(-delivery.MaxClockSkew-time.Second)),
		"garbage header":   delivery.Verify(secret, "v1=zz", tsh, body, now),
		"empty header":     delivery.Verify(secret, "", tsh, body, now),
		"bad timestamp":    delivery.Verify(secret, h, "x", body, now),
		"prefix of header": delivery.Verify(secret, h[:len(h)-2], tsh, body, now),
	} {
		if ok {
			t.Errorf("%s verified", name)
		}
	}
	if !delivery.Verify(secret, h, tsh, body, now.Add(delivery.MaxClockSkew-time.Second)) {
		t.Error("a call inside the window must verify")
	}
	// rotation: the header carries both signatures, and a receiver that knows either secret accepts
	both := delivery.SignatureHeader(secret, other, ts, body)
	if strings.Count(both, "v1=") != 2 || !delivery.Verify(secret, both, tsh, body, now) || !delivery.Verify(other, both, tsh, body, now) {
		t.Fatalf("rotation header %q", both)
	}
}

func TestBackoffGrowsIsBoundedAndJittered(t *testing.T) {
	prev := time.Duration(0)
	for a := 1; a <= 20; a++ {
		d := delivery.Backoff(a, 0)
		if d < prev || d > time.Hour {
			t.Fatalf("attempt %d: %v (previous %v)", a, d, prev)
		}
		prev = d
	}
	if delivery.Backoff(1, 0) != 5*time.Second || delivery.Backoff(2, 0) != 10*time.Second || delivery.Backoff(30, 0) != time.Hour {
		t.Fatalf("schedule: %v %v %v", delivery.Backoff(1, 0), delivery.Backoff(2, 0), delivery.Backoff(30, 0))
	}
	if lo, hi := delivery.Backoff(5, -5), delivery.Backoff(5, 5); lo != time.Duration(float64(delivery.Backoff(5, 0))*0.8) || hi != time.Duration(float64(delivery.Backoff(5, 0))*1.2) {
		t.Fatalf("jitter is not clamped to ±20%%: %v %v", lo, hi)
	}
	if delivery.Backoff(0, 0) != delivery.Backoff(1, 0) {
		t.Fatal("attempt 0 must behave as the first")
	}
}
