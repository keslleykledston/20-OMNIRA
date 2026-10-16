package application

import (
	"strings"
	"testing"
)

func TestTheNoticeGreetsByNameOnlyWhenThereIsOne(t *testing.T) {
	for name, want := range map[string]string{"Maria Silva": "Prezado Maria Silva,", "  Ana\n\nPaula ": "Prezado Ana Paula,", "+5592991882864": "Prezado cliente,", "": "Prezado cliente,"} {
		if got := RenderProtocolOpened(name, "KTG-1005"); !strings.HasPrefix(got, want+"\n\n") {
			t.Errorf("name %q: %q", name, got)
		}
	}
}

func TestTheNoticeCarriesTheProtocolOnOneLineAndTheRightWords(t *testing.T) {
	open, closed := RenderProtocolOpened("Ana", "KTG-1005"), RenderProtocolClosed("Ana", "KTG-1005")
	if !strings.Contains(open, "Seu atendimento foi registrado.") || !strings.Contains(open, "Protocolo de atendimento: KTG-1005\n") {
		t.Fatalf("opened: %q", open)
	}
	if !strings.Contains(closed, "Seu atendimento foi encerrado.") || !strings.Contains(closed, "Protocolo de atendimento: KTG-1005\n") {
		t.Fatalf("closed: %q", closed)
	}
	// neither a hostile name nor protocol can add lines of their own
	evil := RenderProtocolClosed("Ana\nFale com o banco: 123", "KTG-1\nSenha: 9")
	if strings.Contains(evil, "\nFale com o banco") || strings.Contains(evil, "\nSenha") {
		t.Fatalf("injected line: %q", evil)
	}
}
