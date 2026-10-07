package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
)

// exitFx is fakeEffects plus the optional CustomerCloser capability.
type exitFx struct {
	*fakeEffects
	closed []string
}

func (e *exitFx) CloseByCustomer(_ context.Context, _ uuid.UUID, command string) error {
	e.closed = append(e.closed, command)
	return nil
}

const exitSettings = `,"settings":{"customer_exit":{"enabled":true}}`

func menuWithExit(extra string) string {
	edges := edge("1", "start", "next", "m") + "," + edge("2", "m", "tech", "t") + "," + edge("3", "m", "sair", "t") + "," + edge("4", "m", "timeout", "t")
	return wf(`{"id":"start","type":"trigger"},{"id":"m","type":"choice","config":{"text":"Como ajudar?","variable":"topic","options":[{"id":"tech","label":"Suporte"},{"id":"sair","label":"Sair do plano"}]}},{"id":"t","type":"end"}`, edges, extra)
}

func exitWorld(t *testing.T, settings string) (*world, *exitFx) {
	w := newWorld(t, flowSpec{slug: "menu", def: menuWithExit(settings)})
	fx := &exitFx{fakeEffects: w.fx}
	w.eng = NewEngine(w.runs, w.ver, fx, append(AllExecutors(), failExec{})).WithClock(func() time.Time { return w.now })
	w.inbound("oi", true)
	return w, fx
}

func lastSent(fx *exitFx) string {
	if len(fx.sent) == 0 {
		return ""
	}
	return fx.sent[len(fx.sent)-1]
}

func TestTheContactEndsTheAttendanceOnlyAfterAnExplicitYes(t *testing.T) {
	w, fx := exitWorld(t, exitSettings)
	w.inbound("encerrar", false)
	if len(fx.closed) != 0 || !strings.Contains(lastSent(fx), "Quer mesmo encerrar") {
		t.Fatalf("the command must only ASK first: closed=%v last=%q", fx.closed, lastSent(fx))
	}
	w.inbound("sim", false)
	if len(fx.closed) != 1 || !strings.Contains(lastSent(fx), "encerrado a seu pedido") {
		t.Fatalf("after the yes: closed=%v last=%q", fx.closed, lastSent(fx))
	}
	if r := w.onlyRun(); r.Status != domain.RunCancelled {
		t.Fatalf("the run must end, status=%s", r.Status)
	}
}

func TestNoFalsePositives(t *testing.T) {
	// 1) a sentence that merely contains the word never asks anything
	w, fx := exitWorld(t, exitSettings)
	w.inbound("quero encerrar meu contrato", false)
	if strings.Contains(lastSent(fx), "Quer mesmo encerrar") || len(fx.closed) != 0 {
		t.Fatalf("a sentence containing the word is not the command: %q", lastSent(fx))
	}

	// 2) inside the confirmation, "1" and "2" are the numbered answers of THAT question (the text says so): 1 = yes
	w, fx = exitWorld(t, exitSettings)
	w.inbound("encerrar", false)
	if !strings.Contains(lastSent(fx), "1) Sim, encerrar") {
		t.Fatalf("the question must show the numbered answers: %q", lastSent(fx))
	}
	w.inbound("1", false)
	if len(fx.closed) != 1 {
		t.Fatalf("1 answers the confirmation question: closed=%v", fx.closed)
	}

	// 3) "Não" cancels and the menu still works afterwards
	w, fx = exitWorld(t, exitSettings)
	w.inbound("encerrar", false)
	w.inbound("não", false)
	if len(fx.closed) != 0 || !strings.Contains(lastSent(fx), "vamos continuar") {
		t.Fatalf("no: closed=%v last=%q", fx.closed, lastSent(fx))
	}
	w.inbound("Suporte", false)
	if w.runs.execs[len(w.runs.execs)-1].NodeID != "t" {
		t.Fatalf("the menu must still be answered after cancelling")
	}

	// 4) anything that is not an answer cancels the pending close and is processed as an ordinary reply
	w, fx = exitWorld(t, exitSettings)
	w.inbound("encerrar", false)
	w.inbound("Suporte", false)
	if len(fx.closed) != 0 || w.runs.execs[len(w.runs.execs)-1].NodeID != "t" {
		t.Fatalf("a menu answer after the question must route normally and close nothing: closed=%v", fx.closed)
	}

	// 5) a yes that arrives too late is not a yes
	w, fx = exitWorld(t, exitSettings)
	w.inbound("encerrar", false)
	w.now = w.now.Add(time.Duration(domain.ExitPendingTTLSeconds+60) * time.Second)
	w.inbound("sim", false)
	if len(fx.closed) != 0 {
		t.Fatalf("an expired confirmation must not close")
	}

	// 6) an option of the menu the bot is waiting on wins over the command
	w, fx = exitWorld(t, `,"settings":{"customer_exit":{"enabled":true,"commands":["sair do plano"]}}`)
	w.inbound("Sair do plano", false)
	if strings.Contains(lastSent(fx), "Quer mesmo encerrar") || len(fx.closed) != 0 {
		t.Fatalf("the menu option must win: %q", lastSent(fx))
	}
	if w.runs.execs[len(w.runs.execs)-1].NodeID != "t" {
		t.Fatalf("the option must route")
	}

	// 7) off unless the flow turned it on
	w, fx = exitWorld(t, "")
	w.inbound("encerrar", false)
	if len(fx.closed) != 0 || strings.Contains(lastSent(fx), "Quer mesmo encerrar") {
		t.Fatalf("disabled flows ignore the command")
	}
}
