package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	cpfPattern   = regexp.MustCompile(`\b\d{3}\.?\d{3}\.?\d{3}-?\d{2}\b`)
	cnpjPattern  = regexp.MustCompile(`\b\d{2}\.?\d{3}\.?\d{3}/?\d{4}-?\d{2}\b`)
	// a run that starts and ends with a digit and has digits, spaces, dots, dashes and parentheses between: a phone candidate
	phoneCandidate = regexp.MustCompile(`\+?\d[\d\s().\-]{8,}\d`)
)

// MaskPII replaces what identifies a person outside the group - phone numbers, e-mails, CPF and CNPJ - with a neutral label. It runs on
// everything that leaves for the AI provider and on every name the memory keeps (ADR-0050 §7). The stored raw is never masked: only
// what is sent, and what is derived, is minimized.
func MaskPII(s string) string {
	s = emailPattern.ReplaceAllString(s, "[e-mail]")
	s = cnpjPattern.ReplaceAllString(s, "[documento]")
	s = cpfPattern.ReplaceAllString(s, "[documento]")
	return phoneCandidate.ReplaceAllStringFunc(s, func(m string) string {
		n := 0
		for _, r := range m {
			if unicode.IsDigit(r) {
				n++
			}
		}
		// 10 to 15 digits is a phone number; shorter runs are amounts, protocols and dates, which the memory needs to keep
		if n >= 10 && n <= 15 {
			return "[telefone]"
		}
		return m
	})
}

// LooksLikePhone is true when a participant is shown only by a number.
func LooksLikePhone(name string) bool {
	if strings.Contains(MaskPII(name), "[telefone]") {
		return true
	}
	digits, letters := 0, 0
	for _, r := range name {
		switch {
		case unicode.IsDigit(r):
			digits++
		case unicode.IsLetter(r):
			letters++
		}
	}
	return digits >= 8 && letters == 0
}

// Aliaser gives each participant a label for the prompt: the name when it is a name, "Participante N" (stable inside one run) when
// the participant is only a number. Two different numbers never share a label.
type Aliaser struct {
	labels map[string]string
	n      int
}

func NewAliaser() *Aliaser { return &Aliaser{labels: map[string]string{}} }

func (a *Aliaser) Label(author string) string {
	key := strings.TrimSpace(author)
	if v, ok := a.labels[key]; ok {
		return v
	}
	label := CleanActor(key)
	if key == "" || LooksLikePhone(key) {
		a.n++
		label = fmt.Sprintf("Participante %d", a.n)
	}
	a.labels[key] = label
	return label
}
