package application

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxNoticeNameRunes     = 60
	maxNoticeProtocolRunes = 40
)

// The words the customer reads when a protocol opens or ends (ADR-0049 addendum). Fixed text, like the ticket notice: the name comes from the customer's own
// profile and the protocol from the module, and both are reduced to one printable line, so neither can add paragraphs or fake lines.
func greeting(name string) string {
	if n := noticeLine(name, maxNoticeNameRunes); strings.IndexFunc(n, unicode.IsLetter) >= 0 {
		return "Prezado " + n // a name with no letters (a bare phone number) is no way to greet someone
	}
	return "Prezado cliente"
}

// RenderProtocolOpened is the notice of an attendance that just got its protocol.
func RenderProtocolOpened(name, protocol string) string {
	return greeting(name) + ",\n\n" +
		"Seu atendimento foi registrado.\n\n" +
		"Protocolo de atendimento: " + noticeLine(protocol, maxNoticeProtocolRunes) + "\n" +
		"Guarde este número: é com ele que acompanhamos a sua solicitação.\n\n" +
		"Atenciosamente,\n\n" +
		"Equipe de Suporte"
}

// RenderProtocolClosed is the notice of an attendance whose protocol ended.
func RenderProtocolClosed(name, protocol string) string {
	return greeting(name) + ",\n\n" +
		"Seu atendimento foi encerrado.\n\n" +
		"Protocolo de atendimento: " + noticeLine(protocol, maxNoticeProtocolRunes) + "\n" +
		"Se precisar de algo mais, é só responder esta mensagem.\n\n" +
		"Atenciosamente,\n\n" +
		"Equipe de Suporte"
}

// noticeLine collapses every run of whitespace/control characters into a single space and cuts to max runes.
func noticeLine(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || !unicode.IsPrint(r) && r != ' ' {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > max {
		out = string([]rune(out)[:max])
	}
	return strings.TrimSpace(out)
}
