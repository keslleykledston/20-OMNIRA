package delivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// Signature is HMAC-SHA256 over the bytes `<timestamp>.<raw body>` where timestamp is the decimal Unix seconds sent in X-Omnira-Timestamp and raw body is
// EXACTLY the bytes of the HTTP body (not a re-serialization). Header form: `v1=<lowercase hex>`. During a rotation the header carries two values,
// `v1=<current>,v1=<previous>`, and a receiver accepts if either matches.
func Signature(secret []byte, timestamp int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strconv.FormatInt(timestamp, 10)))
	m.Write([]byte{'.'})
	m.Write(body)
	return "v1=" + hex.EncodeToString(m.Sum(nil))
}

// SignatureHeader joins the signature made with the current secret and, while the previous one is still valid, the one made with it.
func SignatureHeader(current []byte, previous []byte, timestamp int64, body []byte) string {
	h := Signature(current, timestamp, body)
	if len(previous) > 0 {
		h += "," + Signature(previous, timestamp, body)
	}
	return h
}

// MaxClockSkew is how old (or how far in the future) a timestamp may be for a receiver to accept it: it bounds the replay window of a captured request.
const MaxClockSkew = 5 * time.Minute

// Verify is the reference check a receiver performs (used by the reference CRM): the timestamp inside the window and a matching signature.
func Verify(secret []byte, header string, timestampHeader string, body []byte, now time.Time) bool {
	ts, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return false
	}
	d := now.Sub(time.Unix(ts, 0))
	if d > MaxClockSkew || d < -MaxClockSkew {
		return false
	}
	want := Signature(secret, ts, body)
	for _, part := range splitComma(header) {
		if hmac.Equal([]byte(part), []byte(want)) {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// Backoff is the wait before attempt number `attempts` (1-based: the wait AFTER the first failure is Backoff(1)): 5 s doubling to a ceiling of one hour,
// spread by `jitter` in [-0.2, +0.2] so a receiver that comes back is not hit by every retry at once.
func Backoff(attempts int, jitter float64) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := 5 * time.Second
	for i := 1; i < attempts && d < time.Hour; i++ {
		d *= 2
	}
	if d > time.Hour {
		d = time.Hour
	}
	if jitter < -0.2 {
		jitter = -0.2
	}
	if jitter > 0.2 {
		jitter = 0.2
	}
	return time.Duration(float64(d) * (1 + jitter))
}

// RetryWindow is how long a fact is retried before it goes to the dead-letter list (configurable at the worker).
const RetryWindow = 72 * time.Hour
