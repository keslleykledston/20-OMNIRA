package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/flowstest"
	"github.com/omnira/omnira/internal/flows/ports"
)

type fakeAuditor struct{ actions []string }

func (f *fakeAuditor) Record(_ context.Context, action string, _ uuid.UUID, _ map[string]any) {
	f.actions = append(f.actions, action)
}

func seedQueue(t *testing.T, env *flowstest.Env, tenant uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := env.Seed.Exec(context.Background(), `INSERT INTO queues(id, tenant_id, name) VALUES($1,$2,$3)`, id, tenant, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func flowJSON(queueID uuid.UUID, extra string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"schema_version":1,"nodes":[
	  {"id":"start","type":"trigger"},
	  {"id":"hi","type":"send_message","config":{"text":"Hello"}},
	  {"id":"q","type":"assign_queue","config":{"queue":"%s"}},
	  {"id":"h","type":"human_handoff"}%s],
	 "edges":[
	  {"id":"e1","source":"start","sourcePort":"next","target":"hi"},
	  {"id":"e2","source":"hi","sourcePort":"next","target":"q"},
	  {"id":"e3","source":"q","sourcePort":"next","target":"h"}]}`, queueID, extra))
}

func TestControlPlaneLifecycleAndCrossTenantReferences(t *testing.T) {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	aud := &fakeAuditor{}
	cp := NewControlPlane(repo, repo, aud)
	queueA := seedQueue(t, env, env.TenantA, "tech-a")
	queueB := seedQueue(t, env, env.TenantB, "tech-b")

	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		f, err := cp.Create(ctx, CreateInput{Slug: "reception", Name: "Reception"})
		if err != nil {
			t.Fatal(err)
		}
		// An empty draft has no trigger: saving is fine (work in progress), publishing is not.
		if _, err := cp.Publish(ctx, f.ID, f.DraftRevision, ""); !errors.Is(err, domain.ErrNotPublishable) {
			t.Fatalf("empty draft must not publish: %v", err)
		}
		var pe *PublishError
		if err := func() error { _, e := cp.Publish(ctx, f.ID, f.DraftRevision, ""); return e }(); !errors.As(err, &pe) || len(pe.Issues) == 0 {
			t.Fatalf("publish error must carry issues: %v", err)
		}

		// A queue of ANOTHER tenant is a reference to something that does not exist here: blocking error.
		res, err := cp.SaveDraft(ctx, f.ID, f.DraftRevision, f.Name, "", flowJSON(queueB, ""))
		if err != nil {
			t.Fatal(err)
		}
		if res.Issues == nil || !hasCode(res.Issues, "resource_not_found") {
			t.Fatalf("cross-tenant queue must be reported while editing: %+v", res.Issues)
		}
		if _, err := cp.Publish(ctx, f.ID, res.Flow.DraftRevision, ""); !errors.Is(err, domain.ErrNotPublishable) {
			t.Fatalf("cross-tenant queue must block publishing: %v", err)
		}

		// With the tenant's own queue it publishes.
		res, err = cp.SaveDraft(ctx, f.ID, res.Flow.DraftRevision, f.Name, "", flowJSON(queueA, ""))
		if err != nil || domain.HasErrors(res.Issues) {
			t.Fatalf("valid draft: %v %+v", err, res.Issues)
		}
		pub, err := cp.Publish(ctx, f.ID, res.Flow.DraftRevision, "go live")
		if err != nil || pub.Version.Version != 1 {
			t.Fatalf("publish: %+v %v", pub, err)
		}
		// Stale revision is refused (the UI validated something else).
		if _, err := cp.Publish(ctx, f.ID, res.Flow.DraftRevision-1, ""); !errors.Is(err, domain.ErrRevisionConflict) {
			t.Fatalf("stale publish: %v", err)
		}
		// Rollback is a pointer move and is audited.
		if _, _, err := cp.Activate(ctx, f.ID, 1); err != nil {
			t.Fatal(err)
		}
		if err := cp.Archive(ctx, f.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := cp.SaveDraft(ctx, f.ID, 99, f.Name, "", flowJSON(queueA, "")); !errors.Is(err, domain.ErrArchived) && !errors.Is(err, domain.ErrRevisionConflict) {
			t.Fatalf("archived flow must not be edited: %v", err)
		}
	})
	want := []string{AuditCreated, AuditSaved, AuditSaved, AuditPublished, AuditActivated, AuditArchived}
	if len(aud.actions) < len(want) {
		t.Fatalf("audit trail incomplete: %v", aud.actions)
	}
	for i, a := range want {
		if aud.actions[i] != a {
			t.Fatalf("audit[%d]=%s want %s (all: %v)", i, aud.actions[i], a, aud.actions)
		}
	}
}

func hasCode(issues []domain.Issue, code string) bool {
	for _, i := range issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

func TestSaveDraftRejectsMalformedDefinitions(t *testing.T) {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	cp := NewControlPlane(repo, repo, nil)
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		f, err := cp.Create(ctx, CreateInput{Slug: "bad", Name: "Bad"})
		if err != nil {
			t.Fatal(err)
		}
		for name, raw := range map[string]string{
			"unknown field": `{"schema_version":1,"nodes":[],"edges":[],"surprise":1}`,
			"wrong version": `{"schema_version":9,"nodes":[],"edges":[]}`,
			"not json":      `{{{`,
		} {
			if _, err := cp.SaveDraft(ctx, f.ID, f.DraftRevision, f.Name, "", json.RawMessage(raw)); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("%s must be rejected as invalid: %v", name, err)
			}
		}
		if issues, _ := cp.ValidateRaw(ctx, []byte(`{{{`)); len(issues) != 1 || issues[0].Code != "invalid_definition" {
			t.Fatalf("live validation must turn a parse failure into an issue: %+v", issues)
		}
	})
}

func TestSettingsRejectForeignChannelLines(t *testing.T) {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	cp := NewControlPlane(repo, repo, nil)
	foreign := uuid.New()
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		f, _ := cp.Create(ctx, CreateInput{Slug: "lines", Name: "Lines"})
		ok := ports.Settings{Priority: 50, RestartPolicy: domain.RestartNewConversationOnly}
		if _, err := cp.UpdateSettings(ctx, f.ID, ok); err != nil {
			t.Fatal(err)
		}
		bad := ok
		bad.TriggerFilter = domain.TriggerFilter{ConnectionIDs: []uuid.UUID{foreign}}
		if _, err := cp.UpdateSettings(ctx, f.ID, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("a line that is not the tenant's must be rejected: %v", err)
		}
		for _, p := range []ports.Settings{{Priority: 0, RestartPolicy: domain.RestartAlways}, {Priority: 5, RestartPolicy: "sometimes"}} {
			if _, err := cp.UpdateSettings(ctx, f.ID, p); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("invalid settings accepted: %+v %v", p, err)
			}
		}
	})
}

func TestSubflowPinningIsFixedAtPublish(t *testing.T) {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	cp := NewControlPlane(repo, repo, nil)
	queue := seedQueue(t, env, env.TenantA, "q")
	subDef := func(text string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"m","type":"send_message","config":{"text":%q}},{"id":"e","type":"end"}],
		 "edges":[{"id":"1","source":"start","sourcePort":"next","target":"m"},{"id":"2","source":"m","sourcePort":"next","target":"e"}]}`, text))
	}
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		sub, _ := cp.Create(ctx, CreateInput{Slug: "greeting", Name: "Greeting", Type: domain.FlowTypeSubflow})
		parent, _ := cp.Create(ctx, CreateInput{Slug: "main", Name: "Main"})
		parentDef := json.RawMessage(fmt.Sprintf(`{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"sub","type":"subflow","config":{"flow":"greeting"}},{"id":"q","type":"assign_queue","config":{"queue":"%s"}},{"id":"h","type":"human_handoff"}],
		 "edges":[{"id":"1","source":"start","sourcePort":"next","target":"sub"},{"id":"2","source":"sub","sourcePort":"next","target":"q"},{"id":"3","source":"q","sourcePort":"next","target":"h"}]}`, queue))
		// Subflow not published yet: blocking error.
		r, err := cp.SaveDraft(ctx, parent.ID, parent.DraftRevision, parent.Name, "", parentDef)
		if err != nil || !hasCode(r.Issues, "subflow_not_found") {
			t.Fatalf("unpublished subflow must be reported: %v %+v", err, r)
		}
		s1, err := cp.SaveDraft(ctx, sub.ID, sub.DraftRevision, sub.Name, "", subDef("v1"))
		if err != nil {
			t.Fatal(err)
		}
		v1, err := cp.Publish(ctx, sub.ID, s1.Flow.DraftRevision, "")
		if err != nil {
			t.Fatalf("publish subflow: %v", err)
		}
		pub, err := cp.Publish(ctx, parent.ID, r.Flow.DraftRevision, "")
		if err != nil {
			t.Fatalf("publish parent: %v", err)
		}
		if pub.Version.SubflowPins["greeting"] != v1.Version.ID {
			t.Fatalf("parent must pin the subflow version active at publish: %+v", pub.Version.SubflowPins)
		}
		// A later publish of the subflow does not change the parent version's pin.
		s2, _ := cp.SaveDraft(ctx, sub.ID, s1.Flow.DraftRevision, sub.Name, "", subDef("v2"))
		v2, err := cp.Publish(ctx, sub.ID, s2.Flow.DraftRevision, "")
		if err != nil || v2.Version.Version != 2 {
			t.Fatalf("publish subflow v2: %v", err)
		}
		again, _ := repo.GetVersion(ctx, pub.Version.ID)
		if again.SubflowPins["greeting"] != v1.Version.ID {
			t.Fatal("a pinned subflow version changed after the subflow was republished")
		}
		// Republishing the unchanged parent now picks the new subflow version (pins differ => a new version).
		pub2, err := cp.Publish(ctx, parent.ID, r.Flow.DraftRevision, "")
		if err != nil || pub2.Version.Version != 2 || pub2.Version.SubflowPins["greeting"] != v2.Version.ID {
			t.Fatalf("republish must follow the new subflow version: %+v %v", pub2, err)
		}
		// A flow cannot call itself.
		selfDef := json.RawMessage(`{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},{"id":"s","type":"subflow","config":{"flow":"main"}}],"edges":[{"id":"1","source":"start","sourcePort":"next","target":"s"},{"id":"2","source":"s","sourcePort":"next","target":"s"}]}`)
		rs, err := cp.SaveDraft(ctx, parent.ID, r.Flow.DraftRevision, parent.Name, "", selfDef)
		if err != nil || !hasCode(rs.Issues, "subflow_self") {
			t.Fatalf("a flow cannot call itself: %v %+v", err, rs)
		}
	})
}
