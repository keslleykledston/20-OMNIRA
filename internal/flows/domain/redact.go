package domain

import (
	"encoding/json"
	"regexp"
	"strings"
)

const redacted = "[redacted]"

// secretInText finds a credential ANYWHERE inside a string (the validator's secretValue is anchored on purpose: author text may
// mention the word "Bearer"). Redact masks just the credential, so "cliente respondeu Bearer abc…" keeps its meaning.
var secretInText = regexp.MustCompile(`(?i)(\bbearer\s+[A-Za-z0-9._~+/=-]{8,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|AKIA[0-9A-Z]{16})`)

var secretValue = regexp.MustCompile(`(?i)(^bearer\s+\S{8,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|AKIA[0-9A-Z]{16})`)

var secretWords = []string{"password", "passwd", "secret", "token", "apikey", "authorization", "bearer", "privatekey", "credential", "cookie"}

// LooksLikeSecretKey is true for field names that must never hold a literal value in a flow ("password", "api_key"...).
// A flow references credentials by id (credential_ref, token_id): names that end in ref/id are references, not secrets.
func LooksLikeSecretKey(k string) bool {
	flat := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(k))
	if strings.HasSuffix(flat, "ref") || strings.HasSuffix(flat, "id") {
		return false
	}
	for _, w := range secretWords {
		if strings.Contains(flat, w) {
			return true
		}
	}
	return false
}

func LooksLikeSecretValue(v string) bool { return secretValue.MatchString(v) }

const maxLoggedString = 2000

// Redact deep-copies v for persistence in the audit trail: sensitive keys and secret-looking values are masked and long
// strings are truncated. Node inputs/outputs go through it before they are stored.
func Redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if LooksLikeSecretKey(k) {
				out[k] = redacted
				continue
			}
			out[k] = Redact(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = Redact(x[i])
		}
		return out
	case string:
		x = secretInText.ReplaceAllString(x, redacted)
		if len(x) > maxLoggedString {
			return strings.ToValidUTF8(x[:maxLoggedString], "") + "…"
		}
		return x
	}
	return v
}

// RedactJSON redacts a JSON document; undecodable input is replaced rather than stored raw.
func RedactJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return json.RawMessage(`{"_":"[unparseable]"}`)
	}
	out, _ := json.Marshal(Redact(v))
	return out
}
