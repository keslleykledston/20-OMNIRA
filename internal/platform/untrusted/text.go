// Package untrusted holds the pure helpers for text that comes from outside OMNIRA's control (a transcript, a document, a
// customer message): cleaning, size limits and the "is this addressed to an AI" signal. Shared by media and intelligence.
package untrusted

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDerivedTextRunes bounds the text stored per attachment (mirrors the column CHECK).
const MaxDerivedTextRunes = 20000

// SanitizeDerivedText cleans text that came out of an engine (a transcript, an OCR result). It is untrusted data:
// control characters and invalid UTF-8 are removed, line breaks normalised, size bounded. It never "fixes" meaning.
func SanitizeDerivedText(in string) string {
	if !utf8.ValidString(in) {
		in = strings.ToValidUTF8(in, "")
	}
	in = strings.ReplaceAll(in, "\r\n", "\n")
	in = strings.ReplaceAll(in, "\r", "\n")
	var b strings.Builder
	b.Grow(len(in))
	for _, r := range in {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), isBidiOrInvisible(r):
			// control characters and bidi overrides can hide or reorder what an operator reads
		default:
			b.WriteRune(r)
		}
	}
	out := multiBlank.ReplaceAllString(strings.TrimSpace(b.String()), "\n\n")
	if utf8.RuneCountInString(out) > MaxDerivedTextRunes {
		out = string([]rune(out)[:MaxDerivedTextRunes])
	}
	return out
}

// isBidiOrInvisible: line/paragraph separators, byte-order marks and the bidirectional overrides and isolates that can
// make an operator read text in a different order than it was written.
func isBidiOrInvisible(r rune) bool {
	switch r {
	case '\u2028', '\u2029', '\ufeff', '\u202a', '\u202b', '\u202c', '\u202d', '\u202e', '\u2066', '\u2067', '\u2068', '\u2069':
		return true
	}
	return false
}

var multiBlank = regexp.MustCompile(`\n{3,}`)

// Whisper-family models invent stock phrases when the audio is silence, noise or music. When the whole output is
// one of these, there was no speech.
var stockHallucinations = []string{
	"legendas pela comunidade amara.org", "legenda pela comunidade amara.org", "obrigado por assistir", "obrigada por assistir",
	"inscreva-se no canal", "se inscreva no canal", "thanks for watching", "thank you for watching", "subtitles by the amara.org community",
	"tchau, tchau", "até a próxima",
}

// IsStockHallucination reports whether the entire text is just a known silence artefact.
func IsStockHallucination(text string) bool {
	t := strings.ToLower(strings.TrimSpace(strings.Trim(text, " .!?\n\t♪")))
	if t == "" {
		return true
	}
	if utf8.RuneCountInString(t) > 60 {
		return false
	}
	for _, h := range stockHallucinations {
		if t == h {
			return true
		}
	}
	return false
}

var instructionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(ignore|ignora|desconsidere|esque[çc]a|disregard|forget)\b.{0,40}\b(instru[çc][õo]es|instructions|prompt|regras|rules)\b`),
	regexp.MustCompile(`(?i)(\bvoc[êe] agora [ée]|\byou are now|\bfrom now on you|\bact as\b|\baja como|\bfinja que)`),
	regexp.MustCompile(`(?i)\b(system prompt|prompt do sistema|developer message|mensagem do sistema)\b`),
	regexp.MustCompile(`(?i)\b(reveal|revele|mostre|print|imprima)\b.{0,30}\b(prompt|instru[çc][õo]es|api[ _-]?key|chave|senha|password|secret|segredo)\b`),
}

// LooksLikeInstruction flags text that appears to be addressed to an AI model. It is a signal for the operator
// and for later automation, never a defence: the defence is that this text is only ever shown as plain data.
func LooksLikeInstruction(text string) bool {
	for _, p := range instructionPatterns {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}
