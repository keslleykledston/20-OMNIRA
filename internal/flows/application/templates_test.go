package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/flowstest"
	"github.com/omnira/omnira/internal/flows/templates"
)

type tplEnv struct {
	t    *testing.T
	env  *flowstest.Env
	repo *adapters.PostgresFlowRepository
	cp   *ControlPlane
	svc  *TemplateService
}

func newTplEnv(t *testing.T) *tplEnv {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	cp := NewControlPlane(repo, repo, nil)
	reg, err := templates.Default()
	if err != nil {
		t.Fatal(err)
	}
	return &tplEnv{t: t, env: env, repo: repo, cp: cp, svc: NewTemplateService(reg, repo, cp, repo, adapters.NewSavepointAtomic(env.App), nil)}
}

func (x *tplEnv) queue(tenant uuid.UUID, name string) string {
	id := uuid.New()
	if _, err := x.env.Seed.Exec(context.Background(), `INSERT INTO queues(id, tenant_id, name) VALUES($1,$2,$3)`, id, tenant, name); err != nil {
		x.t.Fatal(err)
	}
	return id.String()
}

func (x *tplEnv) count(sql string, args ...any) (n int) {
	if err := x.env.Seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		x.t.Fatal(err)
	}
	return
}

func (x *tplEnv) maps(tenant uuid.UUID, keys ...string) map[string]string {
	m := map[string]string{}
	for _, k := range keys {
		m[k] = x.queue(tenant, "q-"+k+"-"+uuid.NewString()[:6])
	}
	return m
}

var starterKeys = []string{"queue.technical", "queue.finance", "queue.commercial", "queue.fallback"}

func TestInstallStarterPackCreatesOnlyTenantOwnedDrafts(t *testing.T) {
	x := newTplEnv(t)
	env := x.env
	maps := x.maps(env.TenantA, starterKeys...)
	var res *InstallResult
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		var err error
		res, err = x.svc.Install(ctx, InstallRequest{Pack: "omnira-starter", Mappings: maps})
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(res.Flows) != 5 || res.PackInstallationID == nil {
		t.Fatalf("the pack's non-optional templates (5) must be installed: %+v", res.Flows)
	}
	if n := x.count(`SELECT count(*) FROM flows WHERE tenant_id=$1 AND status='draft' AND active_version_id IS NULL`, env.TenantA); n != 5 {
		t.Fatalf("everything must be a DRAFT, nothing published: %d", n)
	}
	if n := x.count(`SELECT count(*) FROM flow_versions WHERE tenant_id=$1`, env.TenantA); n != 0 {
		t.Fatalf("an install must never publish: %d versions", n)
	}
	// provenance is recorded as metadata only
	if n := x.count(`SELECT count(*) FROM flows WHERE tenant_id=$1 AND source_template_slug IS NOT NULL AND source_template_version=1 AND template_installed_at IS NOT NULL`, env.TenantA); n != 5 {
		t.Fatalf("provenance missing: %d", n)
	}
	if x.count(`SELECT count(*) FROM flow_pack_installations WHERE tenant_id=$1`, env.TenantA) != 1 || x.count(`SELECT count(*) FROM flow_template_installations WHERE tenant_id=$1 AND pack_installation_id IS NOT NULL`, env.TenantA) != 5 {
		t.Fatal("installation history missing")
	}
	// the installed definitions hold the tenant's own queue ids and no placeholder, no template id
	var def string
	_ = env.Seed.QueryRow(context.Background(), `SELECT draft_definition::text FROM flows WHERE tenant_id=$1 AND slug='smart-reception'`, env.TenantA).Scan(&def)
	if strings.Contains(def, "$ref") || !strings.Contains(def, maps["queue.technical"]) || !strings.Contains(def, maps["queue.fallback"]) {
		t.Fatalf("placeholders must be resolved to the tenant's queues: %s", def)
	}
	// the reception is the tenant's default INBOUND flow
	if x.count(`SELECT count(*) FROM flows WHERE tenant_id=$1 AND slug='smart-reception' AND is_default AND flow_type='INBOUND'`, env.TenantA) != 1 {
		t.Fatal("the reception must become the default flow")
	}
	// another tenant sees none of it
	if x.count(`SELECT count(*) FROM flows WHERE tenant_id=$1`, env.TenantB) != 0 {
		t.Fatal("tenant B has flows it never installed")
	}
	// a second install of the same pack makes numbered COPIES (never overwrites) and does not steal the default
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		again, err := x.svc.Install(ctx, InstallRequest{Pack: "omnira-starter", Mappings: maps})
		if err != nil {
			t.Fatal(err)
		}
		if len(again.Flows) != 5 || again.Flows[0].Slug == "" || len(again.Notes) == 0 {
			t.Fatalf("second install: %+v", again)
		}
	})
	if x.count(`SELECT count(*) FROM flows WHERE tenant_id=$1 AND slug LIKE '%-2'`, env.TenantA) != 5 || x.count(`SELECT count(*) FROM flows WHERE tenant_id=$1 AND is_default`, env.TenantA) != 1 {
		t.Fatal("copies must be numbered and only one default may exist")
	}
}

func TestInstalledPackPublishesAndTheBotServesARealConversation(t *testing.T) {
	x := newTplEnv(t)
	env := x.env
	maps := x.maps(env.TenantA, starterKeys...)
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		if _, err := x.svc.Install(ctx, InstallRequest{Pack: "omnira-starter", Mappings: maps}); err != nil {
			t.Fatal(err)
		}
		// Publish dependencies first, then the reception (a publish pins the subflows' active versions).
		for _, slug := range []string{"unknown-contact", "customer-context", "existing-ticket", "smart-reception"} {
			f, err := x.repo.GetFlowBySlug(ctx, slug)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := x.cp.Publish(ctx, f.ID, f.DraftRevision, "go live"); err != nil {
				t.Fatalf("publish %s: %v", slug, err)
			}
		}
	})
	// A real conversation on a real line, served by the REAL engine over the installed flows.
	rt := &rt{t: t, env: env, repo: x.repo, cp: x.cp, tenant: env.TenantA}
	rt.fx = nil
	line := uuid.New()
	if _, err := env.Seed.Exec(context.Background(), `INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, line, env.TenantA, line.String()); err != nil {
		t.Fatal(err)
	}
	conv, _ := env.SeedConversation(t, env.TenantA, "bot")
	if _, err := env.Seed.Exec(context.Background(), `UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, conv, line); err != nil {
		t.Fatal(err)
	}
	engine := newRealEngine(env, x.repo)
	say := func(text string, isNew bool) {
		t.Helper()
		msg := env.SeedInbound(t, env.TenantA, conv, text)
		env.AsSystem(t, env.TenantA, func(ctx context.Context) {
			if _, err := engine.OnInbound(ctx, InboundEvent{ConversationID: conv, MessageID: msg, NewConversation: isNew}); err != nil {
				t.Fatalf("OnInbound(%q): %v", text, err)
			}
		})
	}
	say("oi", true)          // hello + unknown contact: asks the name
	say("João Silva", false) // asks the company
	say("Acme Ltda", false)  // menu
	say("3", false)          // Comercial -> handoff to the COMMERCIAL queue the admin mapped
	var status, mode string
	var queue *uuid.UUID
	_ = env.Seed.QueryRow(context.Background(), `SELECT status FROM flow_runs WHERE conversation_id=$1`, conv).Scan(&status)
	_ = env.Seed.QueryRow(context.Background(), `SELECT automation_mode, queue_id FROM conversations WHERE id=$1`, conv).Scan(&mode, &queue)
	if status != "waiting_human" || mode != "waiting_human" || queue == nil || queue.String() != maps["queue.commercial"] {
		t.Fatalf("the installed flow must hand the conversation to the mapped commercial queue: run=%s mode=%s queue=%v want %s", status, mode, queue, maps["queue.commercial"])
	}
	// the bot talked as the SYSTEM and nothing was created for the unknown contact: it stays unclassified (a person decides)
	if x.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound' AND sent_by_user_id IS NULL`, conv) < 4 {
		t.Fatal("the bot should have sent its questions")
	}
	if x.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id=$1`, env.TenantA) != 0 {
		t.Fatal("the bot must never create a company for an unknown contact")
	}
}

func TestInstallRefusesMissingAndForeignMappingsBeforeWritingAnything(t *testing.T) {
	x := newTplEnv(t)
	env := x.env
	maps := x.maps(env.TenantA, starterKeys...)
	foreign := x.queue(env.TenantB, "other-tenant")
	delete(maps, "queue.finance")
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		_, err := x.svc.Install(ctx, InstallRequest{Pack: "omnira-starter", Mappings: maps})
		var mm *MissingMappingsError
		if !errors.As(err, &mm) || len(mm.Keys) != 1 || mm.Keys[0] != "queue.finance" || !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("a missing mapping must be reported by key: %v", err)
		}
		maps["queue.finance"] = foreign
		if _, err := x.svc.Install(ctx, InstallRequest{Pack: "omnira-starter", Mappings: maps}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("another tenant's queue must be refused: %v", err)
		}
		maps["queue.finance"] = uuid.NewString()
		if _, err := x.svc.Install(ctx, InstallRequest{Pack: "omnira-starter", Mappings: maps}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("an unknown queue must be refused: %v", err)
		}
		maps["queue.finance"] = x.queue(env.TenantA, "finance")
		maps["queue.typo"] = uuid.NewString()
		if _, err := x.svc.Install(ctx, InstallRequest{Pack: "omnira-starter", Mappings: maps}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("a mapping nobody asked for is a typo and must be refused: %v", err)
		}
		if _, err := x.svc.Install(ctx, InstallRequest{Pack: "omnira-starter", Templates: []string{"not-in-pack"}, Mappings: maps}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("a template outside the pack: %v", err)
		}
		if _, err := x.svc.Install(ctx, InstallRequest{Pack: "no-such-pack", Mappings: maps}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown pack: %v", err)
		}
	})
	if x.count(`SELECT count(*) FROM flows WHERE tenant_id=$1`, env.TenantA) != 0 || x.count(`SELECT count(*) FROM flow_pack_installations WHERE tenant_id=$1`, env.TenantA) != 0 {
		t.Fatal("a refused install must leave nothing behind")
	}
}

// failingRepo breaks the Nth installation record to prove the whole install is undone.
type failingRepo struct {
	*adapters.PostgresFlowRepository
	calls, failAt int
}

func (f *failingRepo) RecordTemplateInstallation(ctx context.Context, t *domain.TemplateInstallation) error {
	f.calls++
	if f.calls == f.failAt {
		return fmt.Errorf("simulated failure on installation %d", f.calls)
	}
	return f.PostgresFlowRepository.RecordTemplateInstallation(ctx, t)
}

func TestInstallIsAllOrNothing(t *testing.T) {
	x := newTplEnv(t)
	env := x.env
	maps := x.maps(env.TenantA, starterKeys...)
	reg, _ := templates.Default()
	failing := &failingRepo{PostgresFlowRepository: x.repo, failAt: 3}
	svc := NewTemplateService(reg, failing, x.cp, x.repo, adapters.NewSavepointAtomic(env.App), nil)
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		if _, err := svc.Install(ctx, InstallRequest{Pack: "omnira-starter", Mappings: maps}); err == nil {
			t.Fatal("the simulated failure must surface")
		}
		// The request transaction is still usable and COMMITS (the middleware commits even when a handler answers 4xx):
		// the savepoint must have undone the partial work.
		if _, err := x.cp.Create(ctx, CreateInput{Slug: "after-failure", Name: "After failure"}); err != nil {
			t.Fatalf("the session must stay usable after a failed install: %v", err)
		}
	})
	if n := x.count(`SELECT count(*) FROM flows WHERE tenant_id=$1 AND slug <> 'after-failure'`, env.TenantA); n != 0 {
		t.Fatalf("a half-installed pack was left behind: %d flows", n)
	}
	if x.count(`SELECT count(*) FROM flow_pack_installations WHERE tenant_id=$1`, env.TenantA) != 0 || x.count(`SELECT count(*) FROM flow_template_installations WHERE tenant_id=$1`, env.TenantA) != 0 {
		t.Fatal("installation records of a failed install must be undone too")
	}
}

func TestSlugCollisionRewritesSubflowReferences(t *testing.T) {
	x := newTplEnv(t)
	env := x.env
	maps := x.maps(env.TenantA, "queue.technical", "queue.finance", "queue.commercial", "queue.projects", "queue.fallback")
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		if _, err := x.cp.Create(ctx, CreateInput{Slug: "after-hours", Name: "Mine", Type: domain.FlowTypeSubflow}); err != nil {
			t.Fatal(err)
		}
		res, err := x.svc.Install(ctx, InstallRequest{Pack: "k3g-support", Mappings: maps})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, f := range res.Flows {
			got[f.Template] = f.Slug
		}
		if got["after-hours"] != "after-hours-2" {
			t.Fatalf("the tenant's own after-hours must be kept and the template installed beside it: %v", got)
		}
		f, err := x.repo.GetFlowBySlug(ctx, "k3g-central-reception")
		if err != nil {
			t.Fatal(err)
		}
		def, err := domain.ParseDefinition(f.DraftDefinition)
		if err != nil {
			t.Fatal(err)
		}
		called := map[string]bool{}
		for _, n := range def.Nodes {
			if n.Type == domain.NodeSubflow {
				c, _ := domain.DecodeConfig[domain.SubflowConfig](n.Config)
				called[c.Flow] = true
			}
		}
		if !called["after-hours-2"] || called["after-hours"] {
			t.Fatalf("the reception must call the copy it was installed with: %v", called)
		}
		mine, _ := x.repo.GetFlowBySlug(ctx, "after-hours")
		if mine.SourceTemplateSlug != nil || mine.Name != "Mine" {
			t.Fatal("the tenant's own flow was touched")
		}
	})
}

func TestInstallOneTemplateBringsItsSubflows(t *testing.T) {
	x := newTplEnv(t)
	env := x.env
	maps := x.maps(env.TenantA, starterKeys...)
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		res, err := x.svc.Install(ctx, InstallRequest{Templates: []string{"smart-reception"}, Mappings: maps})
		if err != nil {
			t.Fatal(err)
		}
		deps, main := 0, 0
		for _, f := range res.Flows {
			if f.Dependency {
				deps++
			} else {
				main++
			}
		}
		if main != 1 || deps != 3 || res.PackInstallationID != nil {
			t.Fatalf("one template + its 3 subflow dependencies, no pack record: %+v", res.Flows)
		}
		// installed as drafts, the dependencies are not published yet: that is a warning, not a blocker
		for _, f := range res.Flows {
			if domain.HasErrors(f.Issues) {
				t.Fatalf("%s: install must not carry blocking errors: %+v", f.Slug, f.Issues)
			}
		}
	})
}

func TestCatalogAndPackPreview(t *testing.T) {
	x := newTplEnv(t)
	p, err := x.svc.Pack("isp-noc", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, m := range p.Mappings {
		keys = append(keys, m.Key)
	}
	if strings.Join(keys, ",") != "queue.commercial,queue.fallback,queue.finance,queue.noc,queue.noc_l2l3" {
		t.Fatalf("each shared queue must be asked ONCE, sorted: %v", keys)
	}
	optional, deps := 0, 0
	for _, it := range p.Items {
		if it.Optional {
			optional++
		}
		if it.Dependency {
			deps++
		}
	}
	if optional != 2 || len(p.Items) < 15 {
		t.Fatalf("items: %+v", p.Items)
	}
	if _, err := x.svc.Pack("isp-noc", 0, []string{"nope"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("selection outside the pack: %v", err)
	}
	if len(x.svc.Templates(TemplateFilter{Category: "ISP"})) < 12 || len(x.svc.Templates(TemplateFilter{RecommendedFor: "msp"})) == 0 || len(x.svc.Templates(TemplateFilter{Query: "vpn"})) == 0 {
		t.Fatal("catalog filters")
	}
	packs, err := x.svc.Packs("isp")
	if err != nil || len(packs) != 2 {
		t.Fatalf("the ISP profile recommends the Starter and ISP NOC packs: %d %v", len(packs), err)
	}
	info, err := x.svc.Template("isp-link-down", 0)
	if err != nil || len(info.Definition) == 0 || info.TestCases == 0 || !json.Valid(info.Definition) {
		t.Fatalf("template preview: %+v %v", info, err)
	}
	if _, err := x.svc.Template("nope", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown template")
	}
}
