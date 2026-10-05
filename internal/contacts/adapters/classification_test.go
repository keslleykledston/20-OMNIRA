package adapters

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	accountsadapters "github.com/omnira/omnira/internal/accounts/adapters"
	accountsdomain "github.com/omnira/omnira/internal/accounts/domain"
	"github.com/omnira/omnira/internal/contacts/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type clsEnv struct {
	t         *testing.T
	seed, app *pgxpool.Pool
	repo      *ClassificationRepository
	accounts  *accountsadapters.PostgresRepository
}

func newClsEnv(t *testing.T) *clsEnv {
	seed, app := seedPool(t), appPool(t)
	return &clsEnv{t: t, seed: seed, app: app, repo: NewClassificationRepository(app), accounts: accountsadapters.NewPostgresRepository(app)}
}

// in runs fn in one tenant session = one transaction, committed at the end (so deferred triggers fire).
func (e *clsEnv) in(tenant, user uuid.UUID, fn func(ctx context.Context)) error {
	return platformdb.WithTenantSession(context.Background(), e.app, user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(ctx, tc))
		return nil
	})
}

// inErr is in() for a body that can fail: an error rolls the whole transaction back, as a failed request does.
func (e *clsEnv) inErr(tenant, user uuid.UUID, fn func(ctx context.Context) error) error {
	return platformdb.WithTenantSession(context.Background(), e.app, user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		return fn(tenancydomain.WithTenantContext(ctx, tc))
	})
}

// exec2 runs a statement as the table owner (fixtures).
func (e *clsEnv) exec2(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *clsEnv) account(tenant, user uuid.UUID, name string) uuid.UUID {
	var id uuid.UUID
	if err := e.in(tenant, user, func(ctx context.Context) {
		a, err := e.accounts.CreateAccount(ctx, tenant, name, accountsdomain.TypeCustomer)
		if err != nil {
			e.t.Fatal(err)
		}
		id = a.ID
	}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *clsEnv) state(contact uuid.UUID) (kind, source string, by *uuid.UUID) {
	if err := e.seed.QueryRow(context.Background(), `SELECT kind, coalesce(classification_source,''), classified_by_user_id FROM contacts WHERE id=$1`, contact).Scan(&kind, &source, &by); err != nil {
		e.t.Fatal(err)
	}
	return
}

func (e *clsEnv) activeLinks(contact uuid.UUID) (n int) {
	_ = e.seed.QueryRow(context.Background(), `SELECT count(*) FROM contact_account_links WHERE contact_id=$1 AND status='active'`, contact).Scan(&n)
	return
}

func link(acc uuid.UUID, primary bool) domain.AccountLinkInput {
	return domain.AccountLinkInput{AccountID: acc, Relationship: domain.RelEmployee, Primary: primary}
}

func TestCustomerTransitionAndLinkAreAtomic(t *testing.T) {
	e := newClsEnv(t)
	a := seedTenant(t, e.seed, "cls")
	u := seedMemberRole(t, e.seed, a, "tenant_agent", "active")
	c := seedContact(t, e.seed, a, "Joana", "+5592911110001", time.Now())
	acme := e.account(a, u, "ACME")

	// unclassified -> customer without any company: refused, nothing changes
	err := e.in(a, u, func(ctx context.Context) {
		if _, err := e.repo.Classify(ctx, a, u, c, domain.KindCustomer, domain.SourceManual, nil, false); !errors.Is(err, domain.ErrCustomerNeedsAccount) {
			t.Errorf("customer without company: %v", err)
		}
	})
	if k, _, _ := e.state(c); err != nil || k != "unclassified" || e.activeLinks(c) != 0 {
		t.Fatalf("a refused transition changed data: kind=%s links=%d err=%v", k, e.activeLinks(c), err)
	}
	// with a company: kind, source, actor and the link land together
	if err := e.in(a, u, func(ctx context.Context) {
		ch, err := e.repo.Classify(ctx, a, u, c, domain.KindCustomer, domain.SourceManual, []domain.AccountLinkInput{link(acme, true)}, false)
		if err != nil || !ch.Changed || ch.PreviousKind != domain.KindUnclassified {
			t.Errorf("classify: %+v %v", ch, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	k, src, by := e.state(c)
	if k != "customer" || src != "manual" || by == nil || *by != u || e.activeLinks(c) != 1 {
		t.Fatalf("kind=%s source=%s by=%v links=%d", k, src, by, e.activeLinks(c))
	}
	// idempotent: classifying again changes nothing and does not duplicate the link
	_ = e.in(a, u, func(ctx context.Context) {
		ch, err := e.repo.Classify(ctx, a, u, c, domain.KindCustomer, domain.SourceManual, []domain.AccountLinkInput{link(acme, true)}, false)
		if err != nil || ch.Changed {
			t.Errorf("replay: %+v %v", ch, err)
		}
	})
	if e.activeLinks(c) != 1 {
		t.Fatal("replay duplicated the link")
	}
}

func TestSecondCompanyKeepsBothAndOnlyOnePrimary(t *testing.T) {
	e := newClsEnv(t)
	a := seedTenant(t, e.seed, "cls2")
	u := seedMemberRole(t, e.seed, a, "tenant_agent", "active")
	c := seedContact(t, e.seed, a, "Joana", "+5592911110002", time.Now())
	acme, xpto := e.account(a, u, "ACME"), e.account(a, u, "XPTO")
	if err := e.in(a, u, func(ctx context.Context) {
		if _, err := e.repo.Classify(ctx, a, u, c, domain.KindCustomer, domain.SourceManual, []domain.AccountLinkInput{link(acme, true)}, false); err != nil {
			t.Fatal(err)
		}
		if _, err := e.repo.LinkAccount(ctx, a, u, c, link(xpto, false), domain.SourceManual); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if e.activeLinks(c) != 2 {
		t.Fatal("the same contact must belong to two companies")
	}
	// making XPTO primary demotes ACME but keeps it active (primary is not exclusive)
	_ = e.in(a, u, func(ctx context.Context) {
		if _, err := e.repo.LinkAccount(ctx, a, u, c, link(xpto, true), domain.SourceManual); err != nil {
			t.Fatal(err)
		}
	})
	var primaries, active int
	_ = e.seed.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE is_primary), count(*) FROM contact_account_links WHERE contact_id=$1 AND status='active'`, c).Scan(&primaries, &active)
	if primaries != 1 || active != 2 {
		t.Fatalf("primaries=%d active=%d", primaries, active)
	}
	var primaryName string
	_ = e.seed.QueryRow(context.Background(), `SELECT a.name FROM contact_account_links l JOIN customer_accounts a ON a.id=l.account_id WHERE l.contact_id=$1 AND l.is_primary AND l.status='active'`, c).Scan(&primaryName)
	if primaryName != "XPTO" {
		t.Fatalf("primary = %s", primaryName)
	}
	// the database refuses a second primary / a duplicate active link outright
	if _, err := e.seed.Exec(context.Background(), `UPDATE contact_account_links SET is_primary=true WHERE contact_id=$1`, c); err == nil {
		t.Fatal("two active primaries must violate the partial unique index")
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO contact_account_links(tenant_id,contact_id,account_id,source) VALUES($1,$2,$3,'manual')`, a, c, acme); err == nil {
		t.Fatal("a duplicate active link must violate the partial unique index")
	}
}

func TestLastLinkCannotBeEndedWithoutReclassifying(t *testing.T) {
	e := newClsEnv(t)
	a := seedTenant(t, e.seed, "cls3")
	u := seedMemberRole(t, e.seed, a, "tenant_agent", "active")
	c := seedContact(t, e.seed, a, "Joana", "+5592911110003", time.Now())
	acme, xpto := e.account(a, u, "ACME"), e.account(a, u, "XPTO")
	var acmeLink, xptoLink uuid.UUID
	if err := e.in(a, u, func(ctx context.Context) {
		if _, err := e.repo.Classify(ctx, a, u, c, domain.KindCustomer, domain.SourceManual, []domain.AccountLinkInput{link(acme, true), link(xpto, false)}, false); err != nil {
			t.Fatal(err)
		}
		ls, _ := e.repo.ListLinks(ctx, a, c, false)
		for _, l := range ls {
			if l.AccountID == acme {
				acmeLink = l.ID
			} else {
				xptoLink = l.ID
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	manual := domain.SourceManual
	// ending one of two is fine, history stays (soft end)
	_ = e.in(a, u, func(ctx context.Context) {
		if _, err := e.repo.EndLink(ctx, a, u, c, xptoLink, nil, ""); err != nil {
			t.Error(err)
		}
	})
	var ended int
	_ = e.seed.QueryRow(context.Background(), `SELECT count(*) FROM contact_account_links WHERE id=$1 AND status='ended' AND ended_at IS NOT NULL`, xptoLink).Scan(&ended)
	if ended != 1 {
		t.Fatal("the link must be ended, not erased")
	}
	// ending the last one is refused and the whole request rolls back: still customer, still linked
	err := e.inErr(a, u, func(ctx context.Context) error {
		_, err := e.repo.EndLink(ctx, a, u, c, acmeLink, nil, "")
		return err
	})
	if !errors.Is(err, domain.ErrLastLink) {
		t.Fatalf("last link: %v", err)
	}
	if k, _, _ := e.state(c); k != "customer" || e.activeLinks(c) != 1 {
		t.Fatalf("a refused removal changed data: kind=%s links=%d", k, e.activeLinks(c))
	}
	// a reclassification target of "customer" does not count as reclassifying
	err = e.inErr(a, u, func(ctx context.Context) error {
		_, err := e.repo.EndLink(ctx, a, u, c, acmeLink, &manual, domain.KindCustomer)
		return err
	})
	if !errors.Is(err, domain.ErrLastLink) {
		t.Fatalf("reclassify to customer: %v", err)
	}
	// ending it AND reclassifying in the same transaction is allowed
	if err := e.inErr(a, u, func(ctx context.Context) error {
		ch, err := e.repo.EndLink(ctx, a, u, c, acmeLink, &manual, domain.KindOther)
		if err == nil && (!ch.Changed || ch.Kind != domain.KindOther) {
			t.Errorf("change: %+v", ch)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if k, src, _ := e.state(c); k != "other" || src != "manual" || e.activeLinks(c) != 0 {
		t.Fatalf("after reclassify: kind=%s source=%s links=%d", k, src, e.activeLinks(c))
	}
	// the database invariant itself: a customer cannot be committed without an active link
	if _, err := e.seed.Exec(context.Background(), `UPDATE contacts SET kind='customer' WHERE id=$1`, c); err == nil {
		t.Fatal("the deferred trigger must refuse a customer without an active link")
	}
}

func TestLinkingRespectsTenantAndArchivedAccounts(t *testing.T) {
	e := newClsEnv(t)
	a, b := seedTenant(t, e.seed, "clsA"), seedTenant(t, e.seed, "clsB")
	ua, ub := seedMemberRole(t, e.seed, a, "tenant_agent", "active"), seedMemberRole(t, e.seed, b, "tenant_admin", "active")
	cA := seedContact(t, e.seed, a, "Joana", "+5592911110004", time.Now())
	accB := e.account(b, ub, "Empresa de B")
	accA := e.account(a, ua, "Empresa de A")
	// tenant A cannot link its contact to tenant B's account, nor classify/link a contact of B
	_ = e.in(a, ua, func(ctx context.Context) {
		if _, err := e.repo.LinkAccount(ctx, a, ua, cA, link(accB, false), domain.SourceManual); !errors.Is(err, domain.ErrAccountMissing) {
			t.Errorf("foreign account: %v", err)
		}
	})
	cB := seedContact(t, e.seed, b, "Contato de B", "+5592911110005", time.Now())
	_ = e.in(a, ua, func(ctx context.Context) {
		if _, err := e.repo.Classify(ctx, a, ua, cB, domain.KindOther, domain.SourceManual, nil, false); !errors.Is(err, domain.ErrContactNotFound) {
			t.Errorf("foreign contact: %v", err)
		}
		if _, err := e.repo.LinkAccount(ctx, a, ua, cB, link(accA, false), domain.SourceManual); !errors.Is(err, domain.ErrContactNotFound) {
			t.Errorf("link foreign contact: %v", err)
		}
	})
	if k, _, _ := e.state(cB); k != "unclassified" {
		t.Fatalf("B's contact was changed: %s", k)
	}
	// an archived account cannot receive new links
	st := accountsdomain.StatusArchived
	_ = e.in(a, ua, func(ctx context.Context) {
		if _, err := e.accounts.UpdateAccount(ctx, a, accA, nil, nil, &st); err != nil {
			t.Fatal(err)
		}
		if _, err := e.repo.LinkAccount(ctx, a, ua, cA, link(accA, false), domain.SourceManual); !errors.Is(err, domain.ErrAccountMissing) {
			t.Errorf("archived account: %v", err)
		}
	})
	// invalid input never reaches the database
	_ = e.in(a, ua, func(ctx context.Context) {
		bad := 1.5
		if _, err := e.repo.LinkAccount(ctx, a, ua, cA, domain.AccountLinkInput{AccountID: accA, Confidence: &bad}, domain.SourceManual); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("confidence: %v", err)
		}
		if _, err := e.repo.LinkAccount(ctx, a, ua, cA, domain.AccountLinkInput{AccountID: accA, Relationship: "boss"}, domain.SourceManual); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("relationship: %v", err)
		}
		if _, err := e.repo.Classify(ctx, a, ua, cA, domain.KindOther, "ai", nil, false); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("source: %v", err)
		}
	})
}
