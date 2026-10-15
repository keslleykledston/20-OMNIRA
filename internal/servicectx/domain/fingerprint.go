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
