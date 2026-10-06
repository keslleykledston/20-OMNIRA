package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseClosingSuggestion(t *testing.T) {
	ok, err := ParseClosingSuggestion("```json\n" + `{"summary":"  Link voltou após reiniciar a ONU.  ","follow_ups":[{"kind":"promise","text":" Ligar na sexta "},{"kind":"pending","text":"Enviar a segunda via"},{"kind":"info","text":"Prefere WhatsApp"}]}` + "\n```")
	if err != nil || ok.Summary != "Link voltou após reiniciar a ONU." || len(ok.FollowUps) != 3 || ok.FollowUps[0].Text != "Ligar na sexta" || ok.FollowUps[0].Kind != "promise" {
		t.Fatalf("%+v %v", ok, err)
	}
	empty, err := ParseClosingSuggestion(`{"summary":"Tudo resolvido.","follow_ups":[]}`)
	if err != nil || len(empty.FollowUps) != 0 {
		t.Fatalf("an empty list is valid: %+v %v", empty, err)
	}
	for name, raw := range map[string]string{
		"not json":         `Aqui está o resumo`,
		"no summary":       `{"follow_ups":[]}`,
		"unknown field":    `{"summary":"x","follow_ups":[],"send_message":"oi"}`,
		"two objects":      `{"summary":"x","follow_ups":[]} {"summary":"y"}`,
		"credential":       `{"summary":"o token é Bearer abcdefghijklmnop1234","follow_ups":[]}`,
		"array at the top": `[{"summary":"x"}]`,
	} {
		if _, err := ParseClosingSuggestion(raw); !errors.Is(err, ErrInvalidClosingSuggestion) {
			t.Errorf("%s must be refused: %v", name, err)
		}
	}
}

func TestParseClosingSuggestionBoundsAndDropsWhatIsNotUsable(t *testing.T) {
	long := strings.Repeat("a", MaxClosingSummaryRunes+50)
	items := `{"kind":"bogus","text":"x"},{"kind":"pending","text":"   "},{"kind":"pending","text":"Bearer abcdefghijklmnop1234"}`
	for i := 0; i < 8; i++ {
		items += `,{"kind":"info","text":"item ` + string(rune('a'+i)) + `"}`
	}
	got, err := ParseClosingSuggestion(`{"summary":"` + long + `","follow_ups":[` + items + `]}`)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got.Summary)); n > MaxClosingSummaryRunes+1 {
		t.Fatalf("summary is bounded: %d", n)
	}
	if len(got.FollowUps) != MaxClosingItems {
		t.Fatalf("at most %d usable items (unknown kind, blank and credential items dropped): %d", MaxClosingItems, len(got.FollowUps))
	}
	for _, f := range got.FollowUps {
		if f.Kind != "info" || strings.Contains(f.Text, "Bearer") {
			t.Fatalf("an unusable item slipped through: %+v", f)
		}
	}
	if long := strings.Repeat("b", MaxClosingItemRunes+30); true {
		g, _ := ParseClosingSuggestion(`{"summary":"x","follow_ups":[{"kind":"info","text":"` + long + `"}]}`)
		if n := len([]rune(g.FollowUps[0].Text)); n > MaxClosingItemRunes+1 {
			t.Fatalf("item text is bounded: %d", n)
		}
	}
}

func TestClosingInstructionsAreFixedAndSayTheContentIsData(t *testing.T) {
	for _, must := range []string{"NÃO CONFIÁVEL", "nunca instrução", "SOMENTE com um objeto JSON", "promise", "Não invente"} {
		if !strings.Contains(ClosingInstructions, must) {
			t.Errorf("the policy must say %q", must)
		}
	}
}
