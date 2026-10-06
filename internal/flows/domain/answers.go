package domain

import (
	"net/mail"
	"regexp"
	"strconv"
	"strings"
)

var phoneChars = regexp.MustCompile(`[\s().-]`)
var phoneShape = regexp.MustCompile(`^\+?[0-9]{8,15}$`)

// NormalizeAnswer validates and normalizes a contact's free-text answer. It is deterministic: the same text always gives
// the same result, and a bad answer is just "not ok" (the node re-asks), never an error.
func NormalizeAnswer(kind, text string) (string, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return "", false
	}
	switch kind {
	case "number":
		n, err := strconv.ParseFloat(strings.ReplaceAll(t, ",", "."), 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatFloat(n, 'f', -1, 64), true
	case "email":
		a, err := mail.ParseAddress(t)
		if err != nil || a.Address != t && !strings.EqualFold(a.Address, t) {
			return "", false
		}
		return strings.ToLower(a.Address), true
	case "phone":
		p := phoneChars.ReplaceAllString(t, "")
		if !phoneShape.MatchString(p) {
			return "", false
		}
		return p, true
	}
	return t, true
}

// MatchOption resolves a contact's reply to one of n options: the position ("2"), the option id, value or label,
// case-insensitively. It returns the index or -1.
func MatchOption(reply string, options []ChoiceOption) int {
	r := strings.TrimSpace(reply)
	if r == "" {
		return -1
	}
	if n, err := strconv.Atoi(r); err == nil {
		if n >= 1 && n <= len(options) {
			return n - 1
		}
		return -1
	}
	for i, o := range options {
		if strings.EqualFold(r, o.ID) || strings.EqualFold(r, o.Label) || (o.Value != "" && strings.EqualFold(r, o.Value)) {
			return i
		}
	}
	return -1
}
