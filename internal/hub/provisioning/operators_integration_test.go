package provisioning_test

// Platform operators (ADR-0038) and the "suspended company is not served" rule (migration 098), on a real PostgreSQL.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// asOperator asks the database, on the user's OWN session, whether `subject` is a platform operator.
func (f *fx) asOperator(session, subject uuid.UUID) bool {
	var ok bool
	f.must(platformdb.WithTenantSession(f.ctx, f.app, session, false, func(c context.Context) error {
		var err error
		ok, err = provisioning.IsPlatformOperator(c, platformdb.QuerierFromContext(c, f.app), subject)
		return err
	}))
	return ok
}

func TestPlatformOperator_GrantRevokeAndVisibility(t *testing.T) {
	f := newFx(t)
	op := f.user("op")
	other := f.user("other")

	if f.asOperator(op, op) || f.asOperator(other, other) {
		t.Fatal("nobody is an operator until the operator tool says so")
	}
	f.must(f.svc.AddPlatformOperator(f.ctx, op))
	f.must(f.svc.AddPlatformOperator(f.ctx, op)) // idempotent
	if n := f.audits("platform.operator.added", op); n != 1 {
		t.Fatalf("exactly one audit event for a repeated grant, got %d", n)
	}
	if !f.asOperator(op, op) {
		t.Fatal("the operator must be recognised on their own session")
	}
	if f.asOperator(other, other) {
		t.Fatal("granting one user must not make anyone else an operator")
	}
	// The function answers only for the session user: it is not an oracle for who the operators are.
	if f.asOperator(other, op) {
		t.Fatal("another user's session must not learn that op is an operator")
	}
	if f.asOperator(op, other) {
		t.Fatal("an operator's session must not be able to ask about someone else either")
	}

	t.Run("the table is invisible to everyone but the user it names", func(t *testing.T) {
		count := func(session uuid.UUID) (n int) {
			f.must(platformdb.WithTenantSession(f.ctx, f.app, session, false, func(c context.Context) error {
				return platformdb.QuerierFromContext(c, f.app).QueryRow(c, `SELECT count(*) FROM platform_operators`).Scan(&n)
			}))
			return n
		}
		if count(other) != 0 {
			t.Fatal("a non-operator can see operator rows")
		}
		if count(op) != 1 {
			t.Fatal("an operator should see only their own row")
		}
	})

	t.Run("nobody can promote themselves or anyone else from an ordinary session", func(t *testing.T) {
		for name, fn := range map[string]string{
			"insert self":   `INSERT INTO platform_operators (user_id, granted_by) SELECT current_user_id(), 'me'`,
			"update revoke": `UPDATE platform_operators SET status = 'active'`,
			"delete":        `DELETE FROM platform_operators`,
		} {
			sql := fn
			var affected int64
			err := platformdb.WithTenantSession(f.ctx, f.app, other, false, func(c context.Context) error {
				tag, e := platformdb.QuerierFromContext(c, f.app).Exec(c, sql)
				affected = tag.RowsAffected()
				return e
			})
			if err == nil && affected != 0 {
				t.Errorf("%s: an ordinary session changed %d row(s)", name, affected)
			}
		}
		if f.asOperator(other, other) {
			t.Fatal("self-promotion worked")
		}
		var rows int
		f.must(f.owner.QueryRow(f.ctx, `SELECT count(*) FROM platform_operators`).Scan(&rows))
		if rows != 1 {
			t.Fatalf("operator rows = %d, want 1", rows)
		}
	})

	t.Run("revoke keeps the record, removes the power, and can be undone", func(t *testing.T) {
		f.must(f.svc.RevokePlatformOperator(f.ctx, op))
		f.must(f.svc.RevokePlatformOperator(f.ctx, op)) // idempotent
		if f.asOperator(op, op) {
			t.Fatal("a revoked operator is still an operator")
		}
		if n := f.audits("platform.operator.revoked", op); n != 1 {
			t.Fatalf("revoke audit rows: %d", n)
		}
		list, err := f.svc.ListPlatformOperators(f.ctx)
		f.must(err)
		if len(list) == 0 || list[len(list)-1].Status != "revoked" && list[0].Status != "revoked" {
			t.Fatalf("the revoked row must stay as a record: %+v", list)
		}
		f.must(f.svc.AddPlatformOperator(f.ctx, op))
		if !f.asOperator(op, op) {
			t.Fatal("re-granting must restore the power")
		}
	})

	t.Run("validation", func(t *testing.T) {
		if err := f.svc.AddPlatformOperator(f.ctx, uuid.New()); !errors.Is(err, provisioning.ErrNotFound) {
			t.Errorf("unknown user: %v", err)
		}
		gone := f.user("gone")
		f.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, gone)
		if err := f.svc.AddPlatformOperator(f.ctx, gone); err == nil {
			t.Error("a disabled user must not become an operator")
		}
		if err := f.svc.RevokePlatformOperator(f.ctx, other); !errors.Is(err, provisioning.ErrNotFound) {
			t.Errorf("revoking a non-operator: %v", err)
		}
	})
}

func TestHubAccess_SuspendedCompanyIsNotServed(t *testing.T) {
	f := newFx(t)
	a := f.tenant("A")
	agent := f.user("agent")
	hub, err := f.svc.CreateHub(f.ctx, "Hub", "")
	f.must(err)
	f.must(f.svc.AddMember(f.ctx, hub, agent, provisioning.RoleAgent))
	_, err = f.svc.CreateContract(f.ctx, provisioning.ContractSpec{Hub: hub, Tenant: a.id})
	f.must(err)
	_, err = f.svc.Grant(f.ctx, provisioning.GrantSpec{Hub: hub, Tenant: a.id, User: agent, CanReply: true})
	f.must(err)

	hubAccess := func(requireReply bool) (ok bool) {
		f.must(platformdb.WithTenantSession(f.ctx, f.app, agent, false, func(c context.Context) error {
			return platformdb.QuerierFromContext(c, f.app).QueryRow(c, `SELECT has_active_hub_access($1, $2, NULL, $3, false, $4)`, agent, a.id, hub, requireReply).Scan(&ok)
		}))
		return ok
	}
	if got := f.reads(agent, a); got != 2 || !hubAccess(false) || !hubAccess(true) {
		t.Fatalf("baseline: reads=%d read=%v reply=%v", got, hubAccess(false), hubAccess(true))
	}
	for _, status := range []string{"suspended", "inactive"} {
		f.exec(`UPDATE tenants SET status = $2 WHERE id = $1`, a.id, status)
		if got := f.reads(agent, a); got != 0 {
			t.Errorf("%s company: the agent still reads %d conversations", status, got)
		}
		if hubAccess(false) || hubAccess(true) {
			t.Errorf("%s company: has_active_hub_access still true", status)
		}
	}
	// Nothing was deleted: re-activating the company restores the access without re-provisioning.
	f.exec(`UPDATE tenants SET status = 'active' WHERE id = $1`, a.id)
	if got := f.reads(agent, a); got != 2 || !hubAccess(true) {
		t.Errorf("re-activated company: reads=%d reply=%v", got, hubAccess(true))
	}
}
