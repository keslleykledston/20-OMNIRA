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

// ADR-0045: a menu that nudges a silent contact (one reminder per silence, each wait a full timeout), then takes the timeout port, which may close the
// attendance for inactivity (a goodbye, then the system closes it) instead of handing it to a human.

type closerFx struct {
	*fakeEffects
	closed []string // reason|note
}

func (c *closerFx) CloseByFlow(_ context.Context, _ uuid.UUID, reason, note string) error {
	c.closed = append(c.closed, reason+"|"+note)
	return nil
}

func reminderMenu(timeoutPort string) string {
	edges := edge("1", "start", "next", "m") + "," + edge("2", "m", "tech", "t") + "," + edge("3", "m", "fin", "t") + "," + edge("4", "m", "timeout", timeoutPort)
	return wf(`{"id":"start","type":"trigger"},
	 {"id":"m","type":"choice","config":{"text":"Como podemos ajudar?","variable":"topic","timeout_seconds":120,"reminders":["Você ainda não escolheu uma opção.","Você ainda está aí? Escolha uma opção para continuarmos."],"options":[{"id":"tech","label":"Suporte"},{"id":"fin","label":"Financeiro"}]}},
	 {"id":"t","type":"end"},
	 {"id":"bye","type":"close_conversation","config":{"text":"Encerramos o atendimento por inatividade. Se precisar, é só escrever.","reason":"no_response"}},
	 {"id":"handoff","type":"human_handoff"}`, edges, "")
}

func reminderWorld(t *testing.T, timeoutPort string) (*world, *closerFx) {
	w := newWorld(t, flowSpec{slug: "menu", def: reminderMenu(timeoutPort)})
	fx := &closerFx{fakeEffects: w.fx}
	w.eng = NewEngine(w.runs, w.ver, fx, append(AllExecutors(), failExec{})).WithClock(func() time.Time { return w.now })
	w.inbound("oi", true)
	return w, fx
}

func fireTimeout(t *testing.T, w *world) {
	t.Helper()
	r := w.onlyRun()
	if r.WaitUntil == nil {
		t.Fatalf("the run is not waiting: %s", r.Status)
	}
	w.now = r.WaitUntil.Add(time.Second)
	if out, err := w.eng.OnTimeout(w.ctx, r.ID); err != nil || out != OutcomeTimedOut {
		t.Fatalf("timeout: %v %v", out, err)
	}
}

func TestSilentContactIsNudgedTwiceThenTheTimeoutPortClosesForInactivity(t *testing.T) {
	w, fx := reminderWorld(t, "bye")
	if len(fx.sent) != 1 {
		t.Fatalf("the menu goes out once: %v", fx.sent)
	}
	first := w.onlyRun().WaitUntil.Sub(w.now)
	if first != 120*time.Second {
		t.Fatalf("each wait is the node's timeout: %s", first)
	}
	fireTimeout(t, w) // 2 min: first reminder, with the menu again
	if r := w.onlyRun(); r.Status != domain.RunWaitingInput || len(fx.sent) != 2 || !strings.Contains(fx.sent[1], "ainda não escolheu") || !strings.Contains(fx.sent[1], "1) Suporte") {
		t.Fatalf("first reminder must carry the menu and keep waiting: status=%s sent=%q", w.onlyRun().Status, fx.sent)
	}
	if len(fx.closed) != 0 {
		t.Fatal("nothing is closed yet")
	}
	fireTimeout(t, w) // 4 min: "still there?" reminder
	if r := w.onlyRun(); r.Status != domain.RunWaitingInput || len(fx.sent) != 3 || !strings.Contains(fx.sent[2], "ainda está aí") || !strings.Contains(fx.sent[2], "2) Financeiro") {
		t.Fatalf("second reminder: status=%s sent=%q", w.onlyRun().Status, fx.sent)
	}
	fireTimeout(t, w) // 6 min: the timeout port -> goodbye + close
	r := w.onlyRun()
	if r.Status != domain.RunCompleted || len(fx.closed) != 1 || !strings.HasPrefix(fx.closed[0], "no_response|") {
		t.Fatalf("the third silence closes for inactivity: status=%s closed=%v", r.Status, fx.closed)
	}
	if last := fx.sent[len(fx.sent)-1]; !strings.Contains(last, "inatividade") || len(fx.sent) != 4 {
		t.Fatalf("goodbye before closing: %q", fx.sent)
	}
}

func TestTimeoutPortCanHandOffToAHumanInstead(t *testing.T) {
	w, fx := reminderWorld(t, "handoff")
	fireTimeout(t, w)
	fireTimeout(t, w)
	fireTimeout(t, w)
	if r := w.onlyRun(); r.Status != domain.RunWaitingHuman || len(fx.closed) != 0 || w.fx.handoffs != 1 {
		t.Fatalf("handoff, not closing: status=%s closed=%v handoffs=%d", r.Status, fx.closed, w.fx.handoffs)
	}
}

func TestAnAnswerAfterAReminderContinuesTheFlow(t *testing.T) {
	w, fx := reminderWorld(t, "bye")
	fireTimeout(t, w)
	w.inbound("2", false)
	if r := w.onlyRun(); r.Status != domain.RunCompleted || len(fx.closed) != 0 {
		t.Fatalf("a valid answer ends the wait normally: %s closed=%v", r.Status, fx.closed)
	}
}

func TestAnInvalidAnswerKeepsTheReminderCount(t *testing.T) {
	w, fx := reminderWorld(t, "bye")
	fireTimeout(t, w) // reminder #1
	w.inbound("hmm", false)
	if r := w.onlyRun(); r.Status != domain.RunWaitingInput {
		t.Fatalf("still waiting: %s", r.Status)
	}
	fireTimeout(t, w) // reminder #2 (not #1 again)
	if !strings.Contains(fx.sent[len(fx.sent)-1], "ainda está aí") {
		t.Fatalf("the count survives an invalid answer: %q", fx.sent)
	}
}

func TestClosingFromAFlowNeedsTheCapabilityAndIsNeverSilent(t *testing.T) {
	w := newWorld(t, flowSpec{slug: "menu", def: reminderMenu("bye")}) // plain fakeEffects: no ConversationCloser
	w.inbound("oi", true)
	fireTimeout(t, w)
	fireTimeout(t, w)
	fireTimeout(t, w)
	if r := w.onlyRun(); r.Status != domain.RunFailed {
		t.Fatalf("without the capability the run fails (and the conversation returns to humans): %s", r.Status)
	}
}

func TestReminderBoundsAreValidated(t *testing.T) {
	good := reminderMenu("bye")
	if issues := validateDefinitionForTest(t, good); len(issues) != 0 {
		t.Fatalf("a good definition has no errors: %v", issues)
	}
	for name, mut := range map[string]func(string) string{
		"no timeout": func(s string) string { return strings.Replace(s, `"timeout_seconds":120,`, "", 1) },
		"too short":  func(s string) string { return strings.Replace(s, `"timeout_seconds":120`, `"timeout_seconds":10`, 1) },
		"empty reminder": func(s string) string {
			return strings.Replace(s, `"Você ainda não escolheu uma opção."`, `"  "`, 1)
		},
		"four reminders":   func(s string) string { return strings.Replace(s, `"reminders":[`, `"reminders":["a","b",`, 1) },
		"bad close reason": func(s string) string { return strings.Replace(s, `"reason":"no_response"`, `"reason":"x"`, 1) },
	} {
		if issues := validateDefinitionForTest(t, mut(good)); len(issues) == 0 {
			t.Errorf("%s must be refused", name)
		}
	}
}

func validateDefinitionForTest(t *testing.T, raw string) []domain.Issue {
	t.Helper()
	def, err := domain.ParseDefinition([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var errs []domain.Issue
	for _, i := range domain.Validate(def, domain.ValidateOptions{}) {
		if i.Severity == domain.SeverityError {
			errs = append(errs, i)
		}
	}
	return errs
}
