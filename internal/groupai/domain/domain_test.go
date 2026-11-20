package domain

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMaskPIIKeepsAmountsAndMasksIdentifiers(t *testing.T) {
	cases := map[string]string{
		"ligue para +55 (92) 99999-1234 hoje":   "ligue para [telefone] hoje",
		"meu email é joao.silva@empresa.com.br": "meu email é [e-mail]",
		"cpf 123.456.789-09 ok":                 "cpf [documento] ok",
		"cnpj 12.345.678/0001-95":               "cnpj [documento]",
		"o valor é 1500 e o protocolo 20240318": "o valor é 1500 e o protocolo 20240318",
		"92 3234-5678":                          "92 3234-5678", // 10 digits with a space: ambiguous short run is masked only when long enough
	}
	for in, want := range cases {
		got := MaskPII(in)
		if in == "92 3234-5678" {
			if got != "[telefone]" { // 10 digits counts as a phone
				t.Errorf("MaskPII(%q) = %q", in, got)
			}
			continue
		}
		if got != want {
			t.Errorf("MaskPII(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAliaserNeverSharesALabelBetweenNumbers(t *testing.T) {
	a := NewAliaser()
	n1, n2 := a.Label("+55 92 99999-1111"), a.Label("+55 92 99999-2222")
	if n1 == n2 || !strings.HasPrefix(n1, "Participante") {
		t.Fatalf("labels %q %q", n1, n2)
	}
	if a.Label("+55 92 99999-1111") != n1 {
		t.Fatal("a participant must keep its label inside a run")
	}
	if a.Label("Maria") != "Maria" {
		t.Fatal("a name stays a name")
	}
}

func TestFingerprintIgnoresCaseAccentsAndPunctuation(t *testing.T) {
	a := Fingerprint(KindRequest, "Enviar a  PLANILHA, até sexta!")
	b := Fingerprint(KindRequest, "enviar a planilha ate sexta")
	if a != b {
		t.Fatal("same words must give the same fingerprint")
	}
	if a == Fingerprint(KindFact, "enviar a planilha ate sexta") {
		t.Fatal("the kind is part of the fingerprint")
	}
}

func TestCleanSummaryRefusesCredentialsAndBoundsLength(t *testing.T) {
	if _, err := CleanSummary("   "); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := CleanSummary("a senha é Bearer abcdefghijklmnopqrstuvwxyz0123456789"); err == nil {
		t.Fatal("a credential must be refused")
	}
	got, err := CleanSummary(strings.Repeat("a", 900))
	if err != nil || len([]rune(got)) > MaxSummaryRunes {
		t.Fatalf("length %d err %v", len([]rune(got)), err)
	}
}

func TestRelevanceDecaysAndProtectedItemsKeepAFloor(t *testing.T) {
	now := time.Now()
	half := DecayedRelevance(4, now.Add(-RelevanceHalfLife), now, false)
	if half < 1.99 || half > 2.01 {
		t.Fatalf("half-life: %v", half)
	}
	if got := DecayedRelevance(1, now.Add(-10*RelevanceHalfLife), now, true); got != 1 {
		t.Fatalf("protected floor: %v", got)
	}
	if got := DecayedRelevance(1, now.Add(-10*RelevanceHalfLife), now, false); got >= 0.01 {
		t.Fatalf("unprotected must fade: %v", got)
	}
	if got := Reinforced(9.8, now, now, false, 0.5); got != MaxRelevance {
		t.Fatalf("clamp: %v", got)
	}
}

func TestVerifiedQuoteNeverPutsWordsInSomeonesMouth(t *testing.T) {
	src := "Pessoal, preciso da nota fiscal do mês de março até sexta."
	if got := VerifiedQuote("NOTA fiscal do mes de marco", src); got != "NOTA fiscal do mes de marco" {
		t.Fatalf("a real excerpt is kept: %q", got)
	}
	if got := VerifiedQuote("eu autorizo o pagamento de 10 mil", src); got != src {
		t.Fatalf("an invented quote is replaced by the real text: %q", got)
	}
}

// ---- extraction

func extCtx() ExtractionContext {
	id := uuid.New()
	return ExtractionContext{
		Messages: map[string]SourceMessage{
			"m1": {Ref: "prov-1", Author: "Maria", Text: "Pode me mandar a nota fiscal de março até sexta?", At: time.Unix(1700000000, 0)},
			"m2": {Ref: "prov-2", Author: "João", Text: "Mandei agora a nota fiscal.", At: time.Unix(1700000100, 0)},
		},
		Items: map[string]Item{
			"i1": {ID: id, Kind: KindRequest, Status: StatusOpen, Truth: TruthAIInferred, Summary: "Maria pediu a nota fiscal"},
			"i2": {ID: uuid.New(), Kind: KindRequest, Status: StatusOpen, Truth: TruthHumanConfirmed, Summary: "protegido"},
			"i3": {ID: uuid.New(), Kind: KindFact, Status: StatusOpen, Truth: TruthAIInferred, Pinned: true, Summary: "fixado"},
		},
		Source: SourceLive,
	}
}

func TestParseExtractionBuildsGroundedItemsOnly(t *testing.T) {
	raw := `{"ops":[
	  {"op":"add","kind":"request","summary":"Maria pediu a nota fiscal de março até sexta","actor":"Maria","status":"open","basis":"said","evidence":[{"ref":"m1","quote":"nota fiscal de março até sexta"}]},
	  {"op":"add","kind":"fact","summary":"sem evidência nenhuma","evidence":[]},
	  {"op":"add","kind":"fact","summary":"evidência de mensagem inexistente","evidence":[{"ref":"m99","quote":"x"}]},
	  {"op":"add","kind":"nada","summary":"tipo inválido","evidence":[{"ref":"m1","quote":"x"}]},
	  {"op":"resolve","id":"i1","evidence":[{"ref":"m2","quote":"Mandei agora a nota fiscal."}]},
	  {"op":"resolve","id":"i2"},
	  {"op":"update","id":"i3","summary":"x"},
	  {"op":"reinforce","id":"i2"},
	  {"op":"resolve","id":"i77"}
	]}`
	ops, stats, err := ParseExtraction(raw, extCtx())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []OpType
	for _, o := range ops {
		kinds = append(kinds, o.Type)
	}
	want := []OpType{OpAdd, OpResolve, OpReinforce}
	if len(kinds) != len(want) {
		t.Fatalf("ops = %v (stats %+v), want %v", kinds, stats, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("ops = %v, want %v", kinds, want)
		}
	}
	add := ops[0]
	if add.Truth != TruthSaidInGroup || add.Evidence[0].Ref != "prov-1" || add.Actor != "Maria" {
		t.Fatalf("add: %+v", add)
	}
	if stats.ProtectedSkipped != 2 { // resolve on a human-confirmed item, update on a pinned one
		t.Fatalf("protected skipped = %d", stats.ProtectedSkipped)
	}
	if stats.Dropped["no_evidence"] != 2 || stats.Dropped["bad_kind"] != 1 || stats.Dropped["unknown_item"] != 1 {
		t.Fatalf("dropped = %v", stats.Dropped)
	}
}

func TestParseExtractionRefusesWhatIsNotAnAnswerAndToleratesWhatAModelHabituallyAdds(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":       "claro, aqui está",
		"empty":          "  ",
		"no ops list":    `{"result":[]}`,
		"ops not a list": `{"ops":"nada"}`,
		"cut off":        `{"ops":[{"op":"add","kind":"fact","summary":"SEGREDO-123`,
		"too large":      `{"ops":[],"pad":"` + strings.Repeat("a", 70000) + `"}`,
	} {
		_, _, err := ParseExtraction(raw, extCtx())
		if !errors.Is(err, ErrInvalidOutput) {
			t.Errorf("%s: must be refused as an invalid answer, got %v", name, err)
		}
		// the error goes to a log: it may say what was wrong, never what the model wrote
		if err != nil && strings.Contains(err.Error(), "SEGREDO") {
			t.Errorf("%s: the error repeats the model's text: %v", name, err)
		}
	}
	for name, raw := range map[string]string{
		"a second object after the first": `{"ops":[]} {"ops":[{"op":"reinforce","id":"i1"}]}`,
		"a field nobody asked for":        `{"ops":[],"notes":"observação","confidence":0.9}`,
		"a sentence before and after":     "Segue a atualização:\n{\"ops\":[]}\nQualquer dúvida, avise.",
		"a fence with a sentence after":   "```json\n{\"ops\":[]}\n```\nObservação final.",
		"an explicitly empty list":        `{"ops":[]}`,
	} {
		if _, _, err := ParseExtraction(raw, extCtx()); err != nil {
			t.Errorf("%s: must be accepted, got %v", name, err)
		}
	}
	// only the first object counts: a second one is never read
	if got, _, _ := ParseExtraction(`{"ops":[]} {"ops":[{"op":"reinforce","id":"i1"}]}`, extCtx()); len(got) != 0 {
		t.Errorf("a second object was applied: %d ops", len(got))
	}
	// a fenced answer is accepted, and the number of ops is capped
	ops := make([]string, 40)
	for i := range ops {
		ops[i] = `{"op":"reinforce","id":"i1"}`
	}
	got, stats, err := ParseExtraction("```json\n{\"ops\":["+strings.Join(ops, ",")+"]}\n```", extCtx())
	if err != nil || len(got) != MaxOpsPerRun || stats.Dropped["over_limit"] != 10 {
		t.Fatalf("cap: %d ops, %v, err %v", len(got), stats.Dropped, err)
	}
}

func TestParseExtractionReplacesAnInventedQuote(t *testing.T) {
	raw := `{"ops":[{"op":"add","kind":"agreement","summary":"combinado","evidence":[{"ref":"m1","quote":"eu aprovo o orçamento de 50 mil"}]}]}`
	ops, stats, err := ParseExtraction(raw, extCtx())
	if err != nil || len(ops) != 1 {
		t.Fatalf("%v %v", ops, err)
	}
	if ops[0].Evidence[0].Quote != CleanQuote(extCtx().Messages["m1"].Text) || stats.EvidenceReplaced != 1 {
		t.Fatalf("quote %q stats %+v", ops[0].Evidence[0].Quote, stats)
	}
}

// ---- consolidation

func TestConsolidationCandidatesLeaveOpenRequestsAndProtectedItemsAlone(t *testing.T) {
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	items := []Item{
		{ID: uuid.New(), Tier: TierEpisodic, Kind: KindRequest, Status: StatusResolved, LastReinforcedAt: old},
		{ID: uuid.New(), Tier: TierEpisodic, Kind: KindRequest, Status: StatusOpen, LastReinforcedAt: old}, // open request: never
		{ID: uuid.New(), Tier: TierEpisodic, Kind: KindFact, Status: StatusOpen, LastReinforcedAt: old},    // standing fact: yes
		{ID: uuid.New(), Tier: TierEpisodic, Kind: KindFact, Status: StatusOpen, LastReinforcedAt: now},    // recent: no
		{ID: uuid.New(), Tier: TierEpisodic, Kind: KindFact, Status: StatusResolved, LastReinforcedAt: old, Pinned: true},
		{ID: uuid.New(), Tier: TierEpisodic, Kind: KindFact, Status: StatusResolved, LastReinforcedAt: old, Truth: TruthHumanConfirmed},
		{ID: uuid.New(), Tier: TierConsolidated, Kind: KindFact, Status: StatusOpen, LastReinforcedAt: old},
		{ID: uuid.New(), Tier: TierEpisodic, Kind: KindFact, Status: StatusSuperseded, LastReinforcedAt: old},
	}
	got := ConsolidationCandidates(items, now)
	if len(got) != 2 || got[0].ID != items[0].ID && got[1].ID != items[0].ID {
		t.Fatalf("candidates = %d", len(got))
	}
}

func TestParseConsolidationRejectsCopiedQuotesAndMasksIdentifiers(t *testing.T) {
	quote := "o cliente precisa da nota fiscal de março para fechar o contador até sexta"
	cands := map[string]Item{
		"i1": {ID: uuid.New(), Kind: KindRequest, Status: StatusResolved, Evidence: []Evidence{{Ref: "p1", Quote: quote}}},
		"i2": {ID: uuid.New(), Kind: KindFact, Status: StatusOpen},
		"i3": {ID: uuid.New(), Kind: KindFact, Status: StatusOpen},
	}
	raw := `{"consolidated":[
	  {"kind":"fact","summary":"` + quote + `","actor":"cliente","replaces":["i1"]},
	  {"kind":"fact","summary":"O financeiro envia nota fiscal todo mês; contato +55 92 99999-1234","actor":"financeiro","replaces":["i2","i9"]},
	  {"kind":"fact","summary":"sem substituir nada","replaces":["i9"]},
	  {"kind":"request","summary":"pedido não consolida","replaces":["i3"]},
	  {"kind":"agreement","summary":"repetido","replaces":["i2"]}
	]}`
	got, stats, err := ParseConsolidation(raw, cands)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Replaces) != 1 {
		t.Fatalf("got %d items, stats %+v", len(got), stats)
	}
	if strings.Contains(got[0].Summary, "99999") || !strings.Contains(got[0].Summary, "[telefone]") {
		t.Fatalf("identifier not masked: %q", got[0].Summary)
	}
	if stats.Dropped["copies_quote"] != 1 || stats.Dropped["bad_kind"] != 1 || stats.Dropped["no_known_replaces"] != 2 {
		t.Fatalf("dropped = %v", stats.Dropped)
	}
}

// ---- proposals and imports

func TestCleanProposal(t *testing.T) {
	title, desc, prio, err := CleanProposal(ProposalTask, "  Enviar\nplanilha  ", "descrição", "")
	if err != nil || title != "Enviar planilha" || desc != "descrição" || prio != "normal" {
		t.Fatalf("%q %q %q %v", title, desc, prio, err)
	}
	for name, f := range map[string]func() error{
		"kind":     func() error { _, _, _, e := CleanProposal("x", "t", "", ""); return e },
		"empty":    func() error { _, _, _, e := CleanProposal(ProposalCase, "  ", "", ""); return e },
		"priority": func() error { _, _, _, e := CleanProposal(ProposalCase, "t", "", "critical"); return e },
		"credential": func() error {
			_, _, _, e := CleanProposal(ProposalCase, "t", "token Bearer abcdefghijklmnopqrstuvwxyz0123456789", "")
			return e
		},
	} {
		if !errors.Is(f(), ErrInvalidProposal) {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestDiscardDecisionIsMechanical(t *testing.T) {
	now := time.Now()
	full := Import{Status: ImportExtracted, BatchesTotal: 3, BatchesOK: 3, ExpiresAt: now.Add(24 * time.Hour)}
	partial := Import{Status: ImportExtracted, BatchesTotal: 3, BatchesOK: 2, BatchesFailed: 1, ExpiresAt: now.Add(24 * time.Hour)}
	pending := Import{Status: ImportUploaded, ExpiresAt: now.Add(24 * time.Hour)}
	expired := Import{Status: ImportFailed, ExpiresAt: now.Add(-time.Second)}

	if r, err := full.DiscardDecision(now, true); err != nil || r != DiscardConfirmed {
		t.Fatalf("confirmed+covered: %v %v", r, err)
	}
	if _, err := full.DiscardDecision(now, false); !errors.Is(err, ErrCoverageIncomplete) {
		t.Fatalf("not confirmed, not expired: %v", err)
	}
	for name, imp := range map[string]Import{"partial": partial, "not extracted": pending} {
		if _, err := imp.DiscardDecision(now, true); !errors.Is(err, ErrCoverageIncomplete) {
			t.Errorf("%s: a confirmation without full coverage must be refused: %v", name, err)
		}
	}
	if r, err := expired.DiscardDecision(now, false); err != nil || r != DiscardExpired {
		t.Fatalf("expired: %v %v", r, err)
	}
	gone := Import{Status: ImportDiscarded, ExpiresAt: now.Add(-time.Hour)}
	if _, err := gone.DiscardDecision(now, true); !errors.Is(err, ErrAlreadyDiscarded) {
		t.Fatalf("already discarded: %v", err)
	}
}

// ---- export parser

const androidChat = "\u200e12/03/2024 08:00 - As mensagens e ligações são protegidas com a criptografia de ponta a ponta.\n" +
	"12/03/2024 08:10 - Maria: Bom dia, preciso da nota fiscal de março.\n" +
	"até sexta, por favor\n" +
	"12/03/2024 08:12 - João: <Mídia oculta>\n" +
	"12/03/2024 08:15 - João adicionou Pedro\n" +
	"12/03/2024 08:20 - Pedro: Esta mensagem foi apagada\n" +
	"13/03/2024 09:00 - João: IMG-2024-03-13.jpg (arquivo anexado)\n" +
	"13/03/2024 09:01 - João: Segue o comprovante\n"

func TestParseWhatsAppExportAndroid(t *testing.T) {
	loc, _ := time.LoadLocation("America/Manaus")
	res, err := ParseWhatsAppExport(androidChat, loc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 7 || len(res.Messages) != 4 || res.DateOrder != "dmy" {
		t.Fatalf("total %d messages %d order %s rejected %v", res.Total, len(res.Messages), res.DateOrder, res.Rejected)
	}
	if res.Rejected["system"] != 2 || res.Rejected["deleted"] != 1 {
		t.Fatalf("rejected %v", res.Rejected)
	}
	m := res.Messages[0]
	if m.Author != "Maria" || m.Text != "Bom dia, preciso da nota fiscal de março.\naté sexta, por favor" {
		t.Fatalf("multi-line message: %+v", m)
	}
	// 08:10 in Manaus (UTC-4) is 12:10 UTC
	if m.At.UTC().Hour() != 12 || m.At.Day() != 12 {
		t.Fatalf("time %v", m.At)
	}
	if !res.Messages[1].Media || res.Messages[1].Text != "" || !res.Messages[2].Media {
		t.Fatalf("media: %+v %+v", res.Messages[1], res.Messages[2])
	}
	if res.Messages[3].Text != "Segue o comprovante" {
		t.Fatalf("last: %+v", res.Messages[3])
	}
}

func TestParseWhatsAppExportIOSAndMonthFirst(t *testing.T) {
	ios := "[3/14/24, 2:35:10 PM] Ana: Reunião confirmada\n[3/15/24, 9:00:00 AM] Bruno: Ok\n"
	res, err := ParseWhatsAppExport(ios, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if res.DateOrder != "mdy" || len(res.Messages) != 2 {
		t.Fatalf("order %s msgs %d", res.DateOrder, len(res.Messages))
	}
	if got := res.Messages[0].At; got.Month() != 3 || got.Day() != 14 || got.Hour() != 14 {
		t.Fatalf("pm time %v", got)
	}
	if got := res.Messages[1].At; got.Hour() != 9 {
		t.Fatalf("am time %v", got)
	}
}

func TestParseWhatsAppExportRefusesWhatIsNotAChat(t *testing.T) {
	for name, in := range map[string]string{
		"empty":     "",
		"prose":     "isto é só um texto qualquer\nsem datas",
		"only sys":  "12/03/2024 08:00 - As mensagens são protegidas.\n",
		"bad dates": "45/13/2024 08:00 - Maria: oi\n",
	} {
		if _, err := ParseWhatsAppExport(in, time.UTC); !errors.Is(err, ErrImportMalformed) {
			t.Errorf("%s: want malformed, got %v", name, err)
		}
	}
	// a subject change that contains ": " is not a person
	res, err := ParseWhatsAppExport("12/03/2024 08:00 - Maria alterou o assunto para \"Fiscal: março\"\n12/03/2024 08:01 - Maria: oi\n", time.UTC)
	if err != nil || len(res.Messages) != 1 || res.Rejected["system"] != 1 {
		t.Fatalf("%v %v", res.Rejected, err)
	}
}

func TestNormalizedRoundTripAndMessageKey(t *testing.T) {
	res, err := ParseWhatsAppExport(androidChat, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeNormalized(EncodeNormalized(res.Messages))
	if err != nil || len(back) != len(res.Messages) {
		t.Fatalf("%d vs %d, %v", len(back), len(res.Messages), err)
	}
	if back[0].Text != res.Messages[0].Text || !back[0].At.Equal(res.Messages[0].At) || !back[1].Media {
		t.Fatalf("round trip lost data: %+v", back[0])
	}
	a := MessageKey("Maria", time.Date(2024, 3, 12, 12, 10, 5, 0, time.UTC), "Bom dia!")
	b := MessageKey("maria", time.Date(2024, 3, 12, 12, 10, 55, 0, time.UTC), "bom dia")
	if a != b {
		t.Fatal("same author, same minute, same words must be the same message")
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestExtractExportText(t *testing.T) {
	text, format, err := ExtractExportText([]byte(androidChat))
	if err != nil || format != "whatsapp_txt" || text != androidChat {
		t.Fatalf("plain: %q %v", format, err)
	}
	z := makeZip(t, map[string]string{"IMG-1.jpg": "not read", "Conversa do WhatsApp com Grupo.txt": androidChat, "_chat.txt": "[3/14/24, 2:35:10 PM] Ana: oi"})
	text, format, err = ExtractExportText(z)
	if err != nil || format != "whatsapp_zip" || !strings.Contains(text, "Ana: oi") {
		t.Fatalf("zip prefers _chat.txt: %q %q %v", text, format, err)
	}
	if _, _, err := ExtractExportText(makeZip(t, map[string]string{"a.jpg": "x"})); !errors.Is(err, ErrImportZip) {
		t.Fatalf("zip without text: %v", err)
	}
	if _, _, err := ExtractExportText([]byte("PK\x03\x04garbage")); !errors.Is(err, ErrImportZip) {
		t.Fatalf("corrupt zip: %v", err)
	}
	if _, _, err := ExtractExportText([]byte("abc\x00def")); !errors.Is(err, ErrImportMalformed) {
		t.Fatalf("binary: %v", err)
	}
	if _, _, err := ExtractExportText(nil); !errors.Is(err, ErrImportMalformed) {
		t.Fatalf("empty: %v", err)
	}
}

func TestExtractExportTextStopsAZipBomb(t *testing.T) {
	bomb := makeZip(t, map[string]string{"_chat.txt": strings.Repeat("0", 8<<20)}) // 8 MiB of zeros compresses ~1000x
	if _, _, err := ExtractExportText(bomb); !errors.Is(err, ErrImportZip) {
		t.Fatalf("zip bomb: %v", err)
	}
}

// ---- findings of the independent review

func TestParserIsLinearOnAHostileFile(t *testing.T) {
	// one header followed by a million blank lines used to cost a quadratic copy (tens of seconds)
	hostile := "12/03/2024 08:10 - Maria: oi\n" + strings.Repeat("\n", 1_000_000) + "12/03/2024 08:11 - João: fim\n"
	start := time.Now()
	res, err := ParseWhatsAppExport(hostile, time.UTC)
	if err != nil || len(res.Messages) != 2 {
		t.Fatalf("%v %d", err, len(res.Messages))
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("parsing a hostile file took %v", d)
	}
	if got := len(res.Messages[0].Text); got > maxRecordBytes {
		t.Fatalf("a message accumulated %d bytes of continuation lines", got)
	}
}

func TestMaskPIICatchesTheFormatsThatSlippedThrough(t *testing.T) {
	for _, in := range []string{
		"+55 11 91234 5678",
		"+55 11 91234‑5678",
		"+55 11 9​1234 5678",
		"5511999998888 5511988887777",
		"11 99999-8888 11 98888-7777",
		"joao@​empresa.com.br",
	} {
		got := MaskPII("contato: " + in + " fim")
		if strings.ContainsAny(got, "0123456789@") && !strings.Contains(got, "[") || strings.Contains(got, "9999") || strings.Contains(got, "1234") || strings.Contains(got, "empresa") {
			t.Errorf("MaskPII(%q) = %q", in, got)
		}
	}
	if got := MaskPII("pagar 1500 até o dia 20 do protocolo 20240318"); got != "pagar 1500 até o dia 20 do protocolo 20240318" {
		t.Errorf("an amount or a protocol was masked: %q", got)
	}
}

func TestChangesToExistingItemsNeedEvidenceAndNeverTouchConsolidatedOnes(t *testing.T) {
	ctx := extCtx()
	ctx.Items["i4"] = Item{ID: uuid.New(), Kind: KindFact, Tier: TierConsolidated, Status: StatusOpen, Truth: TruthAIInferred, Summary: "conhecimento"}
	ctx.Items["i5"] = Item{ID: uuid.New(), Kind: KindRequest, Tier: TierEpisodic, Status: StatusOpen, Truth: TruthAIInferred, Summary: "pedido"}
	raw := `{"ops":[
	  {"op":"resolve","id":"i5"},
	  {"op":"update","id":"i5","summary":"outra coisa","evidence":[]},
	  {"op":"resolve","id":"i4","evidence":[{"ref":"m1","quote":"x"}]},
	  {"op":"update","id":"i4","summary":"x","evidence":[{"ref":"m1","quote":"x"}]},
	  {"op":"reinforce","id":"i4"},
	  {"op":"resolve","id":"i5","evidence":[{"ref":"m2","quote":"Mandei agora a nota fiscal."}]}]}`
	ops, stats, err := ParseExtraction(raw, ctx)
	if err != nil {
		t.Fatal(err)
	}
	var types []OpType
	for _, o := range ops {
		types = append(types, o.Type)
	}
	if len(types) != 2 || types[0] != OpReinforce || types[1] != OpResolve {
		t.Fatalf("ops %v, stats %+v", types, stats)
	}
	if stats.Dropped["no_evidence"] != 2 || stats.Dropped["consolidated_item"] != 2 {
		t.Fatalf("dropped %v", stats.Dropped)
	}
}

func TestOneAnswerCannotMassResolve(t *testing.T) {
	ctx := extCtx()
	var ops []string
	for i := 10; i < 30; i++ {
		h := fmt.Sprintf("i%d", i)
		ctx.Items[h] = Item{ID: uuid.New(), Kind: KindRequest, Tier: TierEpisodic, Status: StatusOpen, Truth: TruthAIInferred, Summary: h}
		ops = append(ops, `{"op":"resolve","id":"`+h+`","evidence":[{"ref":"m2","quote":"x"}]}`)
	}
	got, stats, err := ParseExtraction(`{"ops":[`+strings.Join(ops, ",")+`]}`, ctx)
	if err != nil || len(got) != MaxResolvesPerRun || stats.Dropped["resolve_over_limit"] != 10 {
		t.Fatalf("%d ops, %v, %v", len(got), stats.Dropped, err)
	}
}

func TestAnInvalidAnswerIsDistinguishableAndAConsolidatedFingerprintNeverCollides(t *testing.T) {
	if _, _, err := ParseExtraction("prosa qualquer", extCtx()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("%v", err)
	}
	if _, _, err := ParseConsolidation("prosa qualquer", nil); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("%v", err)
	}
	if FingerprintFor(TierEpisodic, KindFact, "Mesmas palavras") == FingerprintFor(TierConsolidated, KindFact, "mesmas palavras") {
		t.Fatal("a consolidated item must never share the fingerprint of an episodic one")
	}
	if got := VerifiedQuote("Bearer abcdefghijklmnopqrstuvwxyz0123456789", "token Bearer abcdefghijklmnopqrstuvwxyz0123456789 ok"); got != "" {
		t.Fatalf("a quote that looks like a secret was kept: %q", got)
	}
	if CleanActor("Participante 3") != "" || CleanActor("Maria") != "Maria" {
		t.Fatal("the per-batch label must not be stored")
	}
}
