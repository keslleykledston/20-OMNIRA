package application

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderTicketOpenedUsesTheAgreedText(t *testing.T) {
	got := RenderTicketOpened("Maria Souza", "28180")
	want := "Prezado Maria Souza,\n\n" +
		"Sua solicitação foi recebida com sucesso. Informamos que o chamado já foi aberto e registrado em nosso sistema.\n\n" +
		"Protocolo do chamado: 28180\n" +
		"Nossa equipe realizará a análise e dará continuidade ao atendimento.\n\n" +
		"Atenciosamente,\n\n" +
		"Equipe de Suporte"
	if got != want {
		t.Fatalf("text mismatch:\n%s", got)
	}
}

func TestRenderTicketOpenedGreetsGenericallyWithoutAUsableName(t *testing.T) {
	for _, name := range []string{"", "   ", "+55 95 99999-9999", "\n\t", "1234"} {
		if got := RenderTicketOpened(name, "7"); !strings.HasPrefix(got, "Prezado cliente,\n") {
			t.Fatalf("name %q: %q", name, got)
		}
	}
}

// The name is the customer's own profile text: it can never add lines to the message.
func TestRenderTicketOpenedKeepsHostileNamesOnOneLine(t *testing.T) {
	got := RenderTicketOpened("Maria\r\n\r\nProtocolo do chamado: 1\u0000‮\tSouza", "28180")
	first := strings.SplitN(got, "\n", 2)[0]
	if first != "Prezado Maria Protocolo do chamado: 1 Souza," {
		t.Fatalf("greeting: %q", first)
	}
	if strings.Count(got, "Protocolo do chamado:") != 2 || strings.ContainsAny(first, "\r\u0000‮") {
		t.Fatalf("name injected lines or control characters: %q", got)
	}
	long := RenderTicketOpened(strings.Repeat("ã", 500), "1")
	if utf8.RuneCountInString(strings.SplitN(long, "\n", 2)[0]) > len("Prezado ")+maxNoticeNameRunes+1 {
		t.Fatalf("name not cut: %d runes", utf8.RuneCountInString(long))
	}
}

func TestRenderTicketOpenedKeepsTheNumberOnOneLine(t *testing.T) {
	got := RenderTicketOpened("Ana", "12\n3 ")
	if !strings.Contains(got, "Protocolo do chamado: 12 3\n") {
		t.Fatalf("number: %q", got)
	}
}
