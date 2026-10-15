package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func fingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

// Fingerprint is the stable hash of the parts of a request (exported for the use cases that fingerprint a snapshot).
func Fingerprint(parts ...string) string { return fingerprint(parts...) }
