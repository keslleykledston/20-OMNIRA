package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/ports"
)

// choiceFx is fakeEffects plus the optional ChoiceSender capability (what the real effects have on a Meta line).
type choiceFx struct {
	*fakeEffects
	choices []struct {
		text, question string
		options        []ports.ChoiceOption
	}
}

func (c *choiceFx) SendChoice(_ context.Context, _ uuid.UUID, text, question string, options []ports.ChoiceOption, _ string) (ports.SendStatus, error) {
	c.choices = append(c.choices, struct {
		text, question string
		options        []ports.ChoiceOption
	}{text, question, options})
	return ports.SendQueued, nil
}

func menuFlow(style string) string {
	cfg := `"text":"Como ajudar?","variable":"topic","options":[{"id":"tech","label":"Suporte"},{"id":"fin","label":"Financeiro"}]`
	if style != "" {
		cfg += `,"style":"` + style + `"`
	}
	edges := edge("1", "start", "next", "m") + "," + edge("2", "m", "tech", "t") + "," + edge("3", "m", "fin", "f") + "," + edge("4", "m", "timeout", "f")
	return wf(`{"id":"start","type":"trigger"},{"id":"m","type":"choice","config":{`+cfg+`}},{"id":"t","type":"end"},{"id":"f","type":"end"}`, edges, "")
}

func TestMenuGoesOutAsButtonsWhenTheChannelHasThemAndTheTapRoutesByLabel(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "menu", def: menuFlow("")})
	fx := &choiceFx{fakeEffects: w.fx}
	w.eng = NewEngine(w.runs, w.ver, fx, append(AllExecutors(), failExec{})).WithClock(func() time.Time { return w.now })
	w.inbound("oi", true)
	if len(fx.choices) != 1 || len(w.fx.sent) != 0 {
		t.Fatalf("choices=%d text sends=%d, want the menu as ONE interactive send and no plain text", len(fx.choices), len(w.fx.sent))
	}
	c := fx.choices[0]
	if c.question != "Como ajudar?" || c.text != "Como ajudar?\n1) Suporte\n2) Financeiro" || len(c.options) != 2 || c.options[0].ID != "tech" || c.options[0].Title != "Suporte" {
		t.Fatalf("interactive payload: %+v", c)
	}
	// a button tap arrives as the option's title text
	w.inbound("Suporte", false)
	if w.runs.execs[len(w.runs.execs)-1].NodeID != "t" {
		t.Fatalf("the tap must route to the Suporte branch, ended at %s", w.runs.execs[len(w.runs.execs)-1].NodeID)
	}
}

func TestMenuStyleTextAlwaysSendsTheNumberedText(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "menu", def: menuFlow("text")})
	fx := &choiceFx{fakeEffects: w.fx}
	w.eng = NewEngine(w.runs, w.ver, fx, append(AllExecutors(), failExec{})).WithClock(func() time.Time { return w.now })
	w.inbound("oi", true)
	if len(fx.choices) != 0 || len(w.fx.sent) != 1 || w.fx.sent[0] != "Como ajudar?\n1) Suporte\n2) Financeiro" {
		t.Fatalf("choices=%d sent=%v", len(fx.choices), w.fx.sent)
	}
}
