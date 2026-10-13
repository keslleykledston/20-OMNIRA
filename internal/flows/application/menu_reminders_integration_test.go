package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/flows/adapters"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/flowstest"
)

// ADR-0045 on a real PostgreSQL: the silent contact is nudged by durable timers (each wait read from the database like the worker's sweeper does) and the
// last silence closes the attendance through the REAL attendance use case, recorded as the system with the reason no_response.

type closingFx struct {
	*flowstest.RecordingEffects
	real *adapters.PostgresEffects
}

func (c closingFx) CloseByFlow(ctx context.Context, conv uuid.UUID, reason, note string) error {
	return c.real.CloseByFlow(ctx, conv, reason, note)
}

const idleMenuFlow = `{"schema_version":1,"nodes":[
 {"id":"start","type":"trigger"},
 {"id":"m","type":"choice","config":{"text":"Como podemos ajudar?","variable":"topic","timeout_seconds":120,"style":"text","reminders":["Você ainda não escolheu uma opção.","Você ainda está aí? Escolha uma opção."],"options":[{"id":"tech","label":"Suporte"},{"id":"fin","label":"Financeiro"}]}},
 {"id":"ok","type":"end"},
 {"id":"bye","type":"close_conversation","config":{"text":"Encerramos por inatividade.","reason":"no_response"}}],
 "edges":[{"id":"1","source":"start","sourcePort":"next","target":"m"},{"id":"2","source":"m","sourcePort":"tech","target":"ok"},{"id":"3","source":"m","sourcePort":"fin","target":"ok"},
  {"id":"4","source":"m","sourcePort":"timeout","target":"bye"}]}`

func TestSilentMenuIsNudgedByDurableTimersThenClosedForInactivity(t *testing.T) {
	x := newRT(t)
	svc := attendanceapplication.NewService(attendanceadapters.NewPostgresRepository(x.env.App), attendanceadapters.NewAuthorizer(x.env.App),
		attendanceadapters.NewAuditor(auditadapters.NewPostgresAuditEventRepository(x.env.App)))
	real := adapters.NewPostgresEffects(x.env.App, nil).WithCloser(svc)
	x.eng = NewEngine(x.repo, x.repo, closingFx{RecordingEffects: x.fx, real: real}, AllExecutors()).
		WithClock(func() time.Time { x.mu.Lock(); defer x.mu.Unlock(); return x.now }).WithLogger(func(string, ...any) {})
	x.publish("idle-menu", idleMenuFlow)
	if _, err := x.deliver(x.say("oi"), true); err != nil {
		t.Fatal(err)
	}
	fire := func(wantSent int) {
		t.Helper()
		x.advance(121 * time.Second)
		var due []adapters.DueRun
		_ = platformdbSystemAdmin(context.Background(), x.env, func(sc context.Context) error {
			var err error
			due, err = x.repo.DueRuns(sc, x.now, 10)
			return err
		})
		if len(due) != 1 {
			t.Fatalf("one run is due after each silence: %v", due)
		}
		if err := platformdbSystem(context.Background(), x.env, due[0].TenantID, func(sc context.Context) error {
			_, err := x.eng.OnTimeout(sc, due[0].RunID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got := len(x.fx.Sent); got != wantSent {
			t.Fatalf("messages sent so far: %d, want %d: %q", got, wantSent, x.fx.Sent)
		}
	}
	if st, _, _ := x.runStatus(); st != "waiting_input" || len(x.fx.Sent) != 1 {
		t.Fatalf("after the menu: %s %q", st, x.fx.Sent)
	}
	fire(2) // reminder 1 (with the menu)
	if st, _, _ := x.runStatus(); st != "waiting_input" {
		t.Fatalf("still waiting after reminder 1: %s", st)
	}
	fire(3) // reminder 2
	var status string
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT status FROM conversations WHERE id=$1`, x.conv).Scan(&status)
	if status != "open" {
		t.Fatalf("the conversation stays open until the last silence: %s", status)
	}
	fire(4) // the timeout port: goodbye + close
	if st, _, _ := x.runStatus(); st != "completed" {
		t.Fatalf("the run ends: %s", st)
	}
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT status FROM conversations WHERE id=$1`, x.conv).Scan(&status)
	if status != "closed" {
		t.Fatalf("the conversation is closed for inactivity: %s", status)
	}
	var source, reason string
	if err := x.env.Seed.QueryRow(context.Background(), `SELECT source, reason FROM conversation_closures WHERE conversation_id=$1`, x.conv).Scan(&source, &reason); err != nil {
		t.Fatal(err)
	}
	if source != "system" || reason != "no_response" {
		t.Fatalf("closure recorded as the system with the reason no_response: %s %s", source, reason)
	}
}
