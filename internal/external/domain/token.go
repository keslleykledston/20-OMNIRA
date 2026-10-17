package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"strings"
)

// An integration token is "omn_ext_<key_id>.<secret>": key_id is public (16 chars, a-z0-9; it finds the credential), secret is 32 random
// bytes (base64url, 43 chars). Only SHA-256(secret) is stored. 256 bits of entropy make a slow KDF pointless and a digest lookup safe.
const (
	TokenPrefix   = "omn_ext_"
	keyIDLen      = 16
	secretLen     = 43 // base64.RawURLEncoding of 32 bytes
	maxTokenBytes = len(TokenPrefix) + keyIDLen + 1 + secretLen
)

var keyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken generates a token. The plain token is returned once; the caller stores keyID and digest only.
func NewToken() (token, keyID string, digest []byte, err error) {
	kb := make([]byte, 10) // 10 bytes -> 16 base32 chars
	if _, err = rand.Read(kb); err != nil {
		return "", "", nil, err
	}
	keyID = strings.ToLower(keyEncoding.EncodeToString(kb))
	sb := make([]byte, 32)
	if _, err = rand.Read(sb); err != nil {
		return "", "", nil, err
	}
	secret := base64.RawURLEncoding.EncodeToString(sb)
	return TokenPrefix + keyID + "." + secret, keyID, DigestSecret(secret), nil
}

// DigestSecret is SHA-256 of the secret half.
func DigestSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// ParseToken splits a bearer value. It is strict about shape and length so garbage never reaches the database.
func ParseToken(raw string) (keyID, secret string, ok bool) {
	if len(raw) != maxTokenBytes || !strings.HasPrefix(raw, TokenPrefix) {
		return "", "", false
	}
	rest := raw[len(TokenPrefix):]
	keyID, secret = rest[:keyIDLen], rest[keyIDLen+1:]
	if rest[keyIDLen] != '.' {
		return "", "", false
	}
	for _, c := range keyID {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7' || c >= '0' && c <= '9') {
			return "", "", false
		}
	}
	for _, c := range secret {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", "", false
		}
	}
	return keyID, secret, true
}

// SecretMatches compares in constant time.
func SecretMatches(secret string, storedDigest []byte) bool {
	d := DigestSecret(secret)
	return subtle.ConstantTimeCompare(d, storedDigest) == 1
}
