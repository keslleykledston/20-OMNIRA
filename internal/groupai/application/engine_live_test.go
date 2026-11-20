package application

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	aiadapters "github.com/omnira/omnira/internal/ai/adapters"
	aiports "github.com/omnira/omnira/internal/ai/ports"
	"github.com/omnira/omnira/internal/groupai/domain"
)

// TestLiveExtractionAgainstRealModel is an OPT-IN probe: it sends SYNTHETIC messages (never real group data) through the exact prompt the worker
// builds, to the real provider, and checks that the answer parses. It is skipped unless GROUPAI_LIVE_KEY is set; the key is never printed.
//
//	GROUPAI_LIVE_KEY=... GROUPAI_LIVE_MODEL=gemini-3.5-flash-lite go test ./internal/groupai/application -run TestLiveExtraction -v
//
// GROUPAI_LIVE_SHOW=1 prints the model's raw answer (synthetic, so safe to read).
func TestLiveExtractionAgainstRealModel(t *testing.T) {
	key := os.Getenv("GROUPAI_LIVE_KEY")
	if key == "" {
		t.Skip("GROUPAI_LIVE_KEY not set")
	}
	model := os.Getenv("GROUPAI_LIVE_MODEL")
	if model == "" {
		model = "gemini-3.5-flash-lite"
	}
	gen, err := aiadapters.NewTextGenerator("gemini", key, model, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)
	msgs := []sourceMsg{
		{Ref: "m1", Author: "Rafael (cliente)", Text: "Bom dia, preciso da segunda via do boleto de novembro", At: base},
		{Ref: "m2", Author: "Felipe (suporte)", Text: "Bom dia Rafael, já te mando. Qual o CNPJ da empresa?", At: base.Add(2 * time.Minute)},
		{Ref: "m3", Author: "Rafael (cliente)", Text: "É o da matriz, vencimento dia 10", At: base.Add(3 * time.Minute)},
		{Ref: "m4", Author: "Felipe (suporte)", Text: "Pronto, enviei o boleto por e-mail. A janela de manutenção ficou combinada para sábado às 22h.", At: base.Add(9 * time.Minute)},
		{Ref: "m5", Author: "Rafael (cliente)", Text: "Obrigado! Também preciso trocar o certificado digital, pode agendar para a semana que vem?", At: base.Add(12 * time.Minute)},
		{Ref: "m6", Author: "Felipe (suporte)", Text: "Agendado para terça. Qualquer coisa me chame.", At: base.Add(14 * time.Minute)},
	}
	input, ectx := renderExtraction(nil, msgs, "livenonce0001", 6000)
	resp, err := gen.Generate(context.Background(), aiports.GenerateRequest{Instructions: domain.ExtractionInstructions, Input: input, MaxOutputTokens: 3000})
	if err != nil {
		t.Fatalf("provider error: %v", err)
	}
	if os.Getenv("GROUPAI_LIVE_SHOW") != "" {
		fmt.Printf("---- RAW MODEL ANSWER (%d in / %d out tokens) ----\n%s\n---- END ----\n", resp.InputTokens, resp.OutputTokens, resp.OutputText)
	}
	ops, stats, err := domain.ParseExtraction(resp.OutputText, ectx)
	if err != nil {
		t.Fatalf("the answer does not parse: %v\nfirst 400 bytes: %q", err, truncate(resp.OutputText, 400))
	}
	t.Logf("ops=%d stats=%+v", len(ops), stats)
	if len(ops) == 0 {
		t.Fatalf("parsed but produced no operation from a conversation with an open request: stats=%+v", stats)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n])
}
