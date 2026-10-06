package untrusted

import "regexp"

// credentialInText matches the shapes of secrets people paste by accident into free text: a bearer token, a PEM private key
// block, an AWS access key id. It is the same detector the Flow Builder redactor uses (kept separate on purpose so neither
// package depends on the other).
var credentialInText = regexp.MustCompile(`(?i)(\bbearer\s+[A-Za-z0-9._~+/=-]{8,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|AKIA[0-9A-Z]{16})`)

// ContainsCredential reports whether text looks like it carries a credential. Free text that is stored and later shown to
// other people (or sent to a model) is refused when it does.
func ContainsCredential(text string) bool { return credentialInText.MatchString(text) }
