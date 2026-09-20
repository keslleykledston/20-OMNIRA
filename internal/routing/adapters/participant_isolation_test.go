package adapters

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// contacts enforces an E.164 check constraint, so fixtures need a numeric phone
// that stays unique within the run.
var participantPhoneSeq atomic.Int64

func nextParticipantPhone() string {
	return fmt.Sprintf("+5511%09d", participantPhoneSeq.Add(1)+time.Now().Unix()%100000)
}

// participantSeedPool connects as the owner to prepare state directly. The
// owner is a superuser, so it bypasses RLS on purpose — this is never the path
// the application takes.
func participantSeedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping participant isolation tests")
	}
	return openParticipantPool(t, dbURL)
}

// participantAppPool connects as omnira_app, the unprivileged runtime role that
// RLS actually applies to.
func participantAppPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_APP_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_APP_DATABASE_URL not set; skipping RLS-enforced participant tests")
	}
	return openParticipantPool(t, dbURL)
}

func openParticipantPool(t *testing.T, dbURL string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// participantFixture is one tenant with an active member and an open
// conversation, which is the minimum needed to own a participant row.
type participantFixture struct {
	tenantID       uuid.UUID
	userID         uuid.UUID
	conversationID uuid.UUID
}

func seedParticipantFixture(t *testing.T, pool *pgxpool.Pool, membershipStatus string) participantFixture {
	t.Helper()
	ctx := context.Background()
	f := participantFixture{tenantID: uuid.New(), userID: uuid.New(), conversationID: uuid.New()}

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants (id, legal_name, isolation_profile, status)
		 VALUES ($1,$2,'shared_strong_isolation','active')`,
		f.tenantID, "participants-"+f.tenantID.String()[:8]); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, external_subject, email, status) VALUES ($1,$2,$3,'active')`,
		f.userID, f.userID.String(), f.userID.String()+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var roleID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,$5)`,
		uuid.New(), f.tenantID, f.userID, roleID, membershipStatus); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	contactID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1,$2,'Contact',$3)`,
		contactID, f.tenantID, nextParticipantPhone()); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, status) VALUES ($1,$2,$3,'open')`,
		f.conversationID, f.tenantID, contactID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	return f
}

// asTenant runs fn inside a real RLS session for the fixture's user, with the
// TenantContext the repository reads the tenant from.
func asTenant(t *testing.T, pool *pgxpool.Pool, f participantFixture, fn func(ctx context.Context) error) error {
	t.Helper()
	return platformdb.WithTenantSession(context.Background(), pool, f.userID, false, func(sessionCtx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(f.tenantID, f.userID, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		return fn(tenancydomain.WithTenantContext(sessionCtx, tc))
	})
}

func seedParticipantRow(t *testing.T, pool *pgxpool.Pool, f participantFixture) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO conversation_participants (id, tenant_id, conversation_id, user_id, role)
		 VALUES ($1,$2,$3,$4,'INVITED')`,
		id, f.tenantID, f.conversationID, f.userID); err != nil {
		t.Fatalf("seed participant: %v", err)
	}
	return id
}

func TestParticipantsAreScopedToTheSessionTenant(t *testing.T) {
	seed, app := participantSeedPool(t), participantAppPool(t)
	a := seedParticipantFixture(t, seed, "active")
	b := seedParticipantFixture(t, seed, "active")
	seedParticipantRow(t, seed, a)
	seedParticipantRow(t, seed, b)

	repo := NewPostgresParticipantRepository(app)

	if err := asTenant(t, app, a, func(ctx context.Context) error {
		own, err := repo.FindByConversation(ctx, a.conversationID)
		if err != nil {
			return err
		}
		if len(own) != 1 || own[0].TenantID != a.tenantID {
			t.Fatalf("tenant A should see its own participant, got %d rows", len(own))
		}
		// Knowing tenant B's conversation id must be useless.
		foreign, err := repo.FindByConversation(ctx, b.conversationID)
		if err != nil {
			return err
		}
		if len(foreign) != 0 {
			t.Fatalf("tenant A read %d participants of tenant B", len(foreign))
		}
		return nil
	}); err != nil {
		t.Fatalf("tenant A session: %v", err)
	}
}

// Writing into another tenant's conversation must fail, whether RLS refuses the
// row or the composite FK does.
func TestParticipantInsertIntoForeignConversationIsRejected(t *testing.T) {
	seed, app := participantSeedPool(t), participantAppPool(t)
	a := seedParticipantFixture(t, seed, "active")
	b := seedParticipantFixture(t, seed, "active")

	repo := NewPostgresParticipantRepository(app)

	err := asTenant(t, app, a, func(ctx context.Context) error {
		p, buildErr := domain.NewInvite(a.tenantID, b.conversationID, a.userID)
		if buildErr != nil {
			return buildErr
		}
		return repo.Create(ctx, p)
	})
	if err == nil {
		t.Fatal("tenant A inserted a participant into tenant B's conversation")
	}
}

// A participant id of another tenant must not be updatable, even though the id
// is a valid UUID that exists.
func TestParticipantUpdateOfForeignRowIsIneffective(t *testing.T) {
	seed, app := participantSeedPool(t), participantAppPool(t)
	a := seedParticipantFixture(t, seed, "active")
	b := seedParticipantFixture(t, seed, "active")
	foreignID := seedParticipantRow(t, seed, b)

	repo := NewPostgresParticipantRepository(app)
	if err := asTenant(t, app, a, func(ctx context.Context) error {
		return repo.UpdateRole(ctx, foreignID, domain.RoleCoAttendee)
	}); err != nil {
		t.Fatalf("update attempt errored unexpectedly: %v", err)
	}

	var role string
	if err := seed.QueryRow(context.Background(),
		`SELECT role::text FROM conversation_participants WHERE id=$1`, foreignID).Scan(&role); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if role != "INVITED" {
		t.Fatalf("tenant A mutated tenant B's participant: role=%s", role)
	}
}

func TestParticipantRevokedMembershipLosesAccess(t *testing.T) {
	seed, app := participantSeedPool(t), participantAppPool(t)
	f := seedParticipantFixture(t, seed, "revoked")
	seedParticipantRow(t, seed, f)

	repo := NewPostgresParticipantRepository(app)
	if err := asTenant(t, app, f, func(ctx context.Context) error {
		rows, err := repo.FindByConversation(ctx, f.conversationID)
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			t.Fatalf("revoked membership still read %d participants", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatalf("revoked session: %v", err)
	}
}

// The repository never deletes: leaving is a soft update of left_at. The
// runtime role must not hold the privilege either.
func TestParticipantDeleteIsDeniedToRuntimeRole(t *testing.T) {
	seed, app := participantSeedPool(t), participantAppPool(t)
	f := seedParticipantFixture(t, seed, "active")
	id := seedParticipantRow(t, seed, f)

	err := asTenant(t, app, f, func(ctx context.Context) error {
		_, execErr := platformdb.QuerierFromContext(ctx, app).Exec(ctx,
			`DELETE FROM conversation_participants WHERE id=$1`, id)
		return execErr
	})
	if err == nil {
		t.Fatal("runtime role was allowed to DELETE a participant")
	}

	var alive bool
	if err := seed.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM conversation_participants WHERE id=$1)`, id).Scan(&alive); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !alive {
		t.Fatal("participant row was deleted despite the denial")
	}
}

func TestParticipantsTableEnforcesRLS(t *testing.T) {
	app := participantAppPool(t)
	ctx := context.Background()

	var rls, force bool
	if err := app.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname='conversation_participants'`).
		Scan(&rls, &force); err != nil {
		t.Fatalf("inspect table: %v", err)
	}
	if !rls || !force {
		t.Fatalf("conversation_participants must have RLS and FORCE RLS, got rls=%v force=%v", rls, force)
	}

	var isSuper, bypassRLS bool
	if err := app.QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&isSuper, &bypassRLS); err != nil {
		t.Fatalf("inspect runtime role: %v", err)
	}
	if isSuper || bypassRLS {
		t.Fatalf("runtime role must be NOSUPERUSER/NOBYPASSRLS, got super=%v bypassrls=%v", isSuper, bypassRLS)
	}
}
