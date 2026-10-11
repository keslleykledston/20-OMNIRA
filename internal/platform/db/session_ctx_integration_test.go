package db

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/sessionctx"
	"github.com/omnira/omnira/internal/testhelpers"
)

// ADR-0042: the attacks. Everything here runs as the real application role (non-superuser, FORCE RLS) against the migrated schema, and proves what arbitrary SQL in an
// application session can and cannot do to the identity the policies read. The gate runs the whole suite in mode enforce; these tests switch the mode themselves and
// put it back.

type ctxLab struct {
	t     *testing.T
	ctx   context.Context
	owner *pgxpool.Pool
	app   *pgxpool.Pool
	key   []byte
}

func newCtxLab(t *testing.T) *ctxLab {
	t.Helper()
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	key, err := sessionctx.Key()
	if err != nil || key == nil {
		t.Skipf("no %s configured (the gate sets it): nothing to sign with (%v)", sessionctx.KeyEnv, err)
	}
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	cfg, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 4
	app, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	l := &ctxLab{t: t, ctx: ctx, owner: owner, app: app, key: key}
	var bypass bool
	if err := app.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass); err != nil || bypass {
		t.Fatalf("the application role must not bypass RLS (bypass=%v err=%v)", bypass, err)
	}
	return l
}

func (l *ctxLab) mode() string {
	var m string
	if err := l.owner.QueryRow(l.ctx, `SELECT mode FROM session_ctx_mode`).Scan(&m); err != nil {
		l.t.Fatal(err)
	}
	return m
}

// withMode switches the verification mode for the rest of the test and restores the previous one.
func (l *ctxLab) withMode(mode string) {
	l.t.Helper()
	prev := l.mode()
	if _, err := l.owner.Exec(l.ctx, `SELECT ctx_set_mode($1)`, mode); err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { _, _ = l.owner.Exec(context.Background(), `SELECT ctx_set_mode($1)`, prev) })
}

// tx runs fn in a transaction of the application role that is rolled back at the end.
func (l *ctxLab) tx(fn func(tx pgx.Tx)) {
	l.t.Helper()
	tx, err := l.app.Begin(l.ctx)
	if err != nil {
		l.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(l.ctx) }()
	fn(tx)
}

func (l *ctxLab) identity(tx pgx.Tx) (user *uuid.UUID, system bool) {
	l.t.Helper()
	if err := tx.QueryRow(l.ctx, `SELECT current_user_id(), is_system_admin()`).Scan(&user, &system); err != nil {
		l.t.Fatal(err)
	}
	return user, system
}

func (l *ctxLab) begin(tx pgx.Tx, raw string) bool {
	l.t.Helper()
	var ok bool
	if err := tx.QueryRow(l.ctx, `SELECT ctx_begin($1)`, raw).Scan(&ok); err != nil {
		l.t.Fatal(err)
	}
	return ok
}

func (l *ctxLab) forge(tx pgx.Tx, user uuid.UUID, system bool) {
	l.t.Helper()
	if _, err := tx.Exec(l.ctx, `SELECT set_config('app.current_user_id', $1, true), set_config('app.is_system_admin', $2, true)`, user.String(), fmt.Sprint(system)); err != nil {
		l.t.Fatal(err)
	}
}

func otherKey() []byte {
	k, _ := hex.DecodeString(strings.Repeat("c3", 32))
	return k
}

func TestArbitrarySQLCannotForgeTheSessionIdentityInEnforceMode(t *testing.T) {
	l := newCtxLab(t)
	l.withMode("enforce")
	user := uuid.New()

	t.Run("setting the legacy settings is not an identity", func(t *testing.T) {
		l.tx(func(tx pgx.Tx) {
			l.forge(tx, user, true)
			if u, sys := l.identity(tx); u != nil || sys {
				t.Fatalf("forged settings were believed: user=%v system=%v", u, sys)
			}
		})
	})

	t.Run("a context signed with another key is refused", func(t *testing.T) {
		l.tx(func(tx pgx.Tx) {
			if l.begin(tx, sessionctx.Sign(otherKey(), user, true, time.Now())) {
				t.Fatal("a context signed with a key the database does not know was accepted")
			}
			if u, sys := l.identity(tx); u != nil || sys {
				t.Fatalf("identity from a refused context: user=%v system=%v", u, sys)
			}
		})
	})

	t.Run("a genuine context is believed, and only for what it says", func(t *testing.T) {
		l.tx(func(tx pgx.Tx) {
			if !l.begin(tx, sessionctx.Sign(l.key, user, false, time.Now())) {
				t.Fatal("a genuine context was refused")
			}
			l.forge(tx, uuid.New(), true) // forging on top of a genuine, non-system context changes nothing
			if u, sys := l.identity(tx); u == nil || *u != user || sys {
				t.Fatalf("want user %s, not system; got user=%v system=%v", user, u, sys)
			}
		})
		l.tx(func(tx pgx.Tx) {
			if !l.begin(tx, sessionctx.Sign(l.key, uuid.Nil, true, time.Now())) {
				t.Fatal("a genuine system context was refused")
			}
			if u, sys := l.identity(tx); u != nil || !sys {
				t.Fatalf("want system without a user; got user=%v system=%v", u, sys)
			}
		})
	})

	t.Run("tampering with a signed context breaks it", func(t *testing.T) {
		good := sessionctx.Sign(l.key, user, false, time.Now())
		parts := strings.Split(good, "|")
		cases := map[string]string{
			"system flag flipped":  strings.Join([]string{parts[0], parts[1], "1", parts[3], parts[4]}, "|"),
			"another user":         strings.Join([]string{parts[0], uuid.NewString(), parts[2], parts[3], parts[4]}, "|"),
			"issued later":         strings.Join([]string{parts[0], parts[1], parts[2], fmt.Sprint(time.Now().Add(time.Second).Unix()), parts[4]}, "|"),
			"signature truncated":  good[:len(good)-2],
			"signature uppercased": strings.Join([]string{parts[0], parts[1], parts[2], parts[3], strings.ToUpper(parts[4])}, "|"),
			"extra field":          good + "|x",
			"empty":                "",
			"garbage":              "b1|||",
			"wrong tag":            "b2" + good[2:],
		}
		for name, raw := range cases {
			l.tx(func(tx pgx.Tx) {
				if l.begin(tx, raw) {
					t.Errorf("%s: a tampered context was accepted", name)
				}
				if u, sys := l.identity(tx); u != nil || sys {
					t.Errorf("%s: identity from a tampered context: user=%v system=%v", name, u, sys)
				}
			})
		}
	})

	t.Run("a stale or future context is refused", func(t *testing.T) {
		for name, at := range map[string]time.Time{"ten minutes old": time.Now().Add(-10 * time.Minute), "five minutes ahead": time.Now().Add(5 * time.Minute)} {
			l.tx(func(tx pgx.Tx) {
				if l.begin(tx, sessionctx.Sign(l.key, user, true, at)) {
					t.Errorf("%s: accepted", name)
				}
			})
		}
		l.tx(func(tx pgx.Tx) { // inside the tolerance
			if !l.begin(tx, sessionctx.Sign(l.key, user, false, time.Now().Add(-4*time.Minute))) {
				t.Error("a context issued four minutes ago must still be accepted")
			}
		})
	})

	t.Run("the verified identity does not outlive its transaction", func(t *testing.T) {
		one, err := pgxpool.ParseConfig(l.app.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		one.MaxConns = 1 // the same backend serves every transaction below
		pool, err := pgxpool.NewWithConfig(l.ctx, one)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		if err := WithTenantSession(l.ctx, pool, user, true, func(sc context.Context) error {
			var sys bool
			if err := QuerierFromContext(sc, pool).QueryRow(sc, `SELECT is_system_admin()`).Scan(&sys); err != nil || !sys {
				t.Errorf("inside the session: system=%v err=%v", sys, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// the next transaction of the same backend, without a context
		tx, err := pool.Begin(l.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(l.ctx) }()
		if u, sys := l.identity(tx); u != nil || sys {
			t.Fatalf("the previous transaction's identity leaked into the next one: user=%v system=%v", u, sys)
		}
	})

	t.Run("a forged identity reads nothing through RLS, a genuine one does", func(t *testing.T) {
		tenant := uuid.New()
		if _, err := l.owner.Exec(l.ctx, `INSERT INTO tenants (id, legal_name, status) VALUES ($1, 'ctx lab', 'active')`, tenant); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = l.owner.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant) })
		count := func(tx pgx.Tx) int {
			var n int
			if err := tx.QueryRow(l.ctx, `SELECT count(*) FROM tenants WHERE id = $1`, tenant).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		l.tx(func(tx pgx.Tx) {
			l.forge(tx, uuid.New(), true)
			if n := count(tx); n != 0 {
				t.Errorf("a forged administrator read %d tenant(s)", n)
			}
		})
		l.tx(func(tx pgx.Tx) {
			l.begin(tx, sessionctx.Sign(l.key, uuid.Nil, true, time.Now()))
			if n := count(tx); n != 1 {
				t.Errorf("a genuine system context read %d tenant(s), want 1", n)
			}
		})
	})
}

func TestTheApplicationRoleCannotReachTheKeyTheRegistryOrTheInternals(t *testing.T) {
	l := newCtxLab(t)
	l.withMode("enforce")

	t.Run("no privilege on the three tables", func(t *testing.T) {
		for _, tbl := range []string{"session_ctx_keys", "session_ctx_mode", "session_ctx_verified"} {
			for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
				var has bool
				if err := l.app.QueryRow(l.ctx, `SELECT has_table_privilege(current_user, $1, $2)`, tbl, priv).Scan(&has); err != nil {
					t.Fatal(err)
				}
				if has {
					t.Errorf("the application role holds %s on %s", priv, tbl)
				}
			}
			var n int
			if err := l.app.QueryRow(l.ctx, `SELECT count(*) FROM `+tbl).Scan(&n); err == nil {
				t.Errorf("the application role could read %s", tbl)
			}
		}
	})

	t.Run("cannot write the registry to make itself somebody", func(t *testing.T) {
		l.tx(func(tx pgx.Tx) {
			if _, err := tx.Exec(l.ctx, `INSERT INTO session_ctx_verified (pid, txstart, uid, sys) VALUES (pg_backend_pid(), now(), gen_random_uuid(), true)`); err == nil {
				t.Error("inserted into the verified registry")
			}
		})
		l.tx(func(tx pgx.Tx) {
			if _, err := tx.Exec(l.ctx, `UPDATE session_ctx_mode SET mode = 'off'`); err == nil {
				t.Error("changed the mode")
			}
		})
	})

	t.Run("cannot call the owner-only and internal functions", func(t *testing.T) {
		for _, fn := range []string{
			"ctx_install_key(bytea)", "ctx_drop_previous_key()", "ctx_set_mode(text)", "ctx_install_readers(text)", "ctx_signed(text,text,integer)",
			"ctx_set_acting(uuid,uuid)", "ctx_measure(text,text,text)", "ctx_verified(text)",
		} {
			var can bool
			if err := l.app.QueryRow(l.ctx, `SELECT has_function_privilege(current_user, $1, 'EXECUTE')`, fn).Scan(&can); err != nil {
				t.Fatalf("%s: %v", fn, err)
			}
			if can {
				t.Errorf("the application role can execute %s", fn)
			}
		}
	})

	t.Run("cannot plant objects the verifier could pick up", func(t *testing.T) {
		var canCreate bool
		if err := l.app.QueryRow(l.ctx, `SELECT has_schema_privilege(current_user, 'public', 'CREATE')`).Scan(&canCreate); err != nil || canCreate {
			t.Errorf("the application role can create objects in public (can=%v err=%v)", canCreate, err)
		}
		// a temp function shadowing a built-in the verifier calls must not matter: the verifier resolves names through pg_catalog first
		l.tx(func(tx pgx.Tx) {
			if _, err := tx.Exec(l.ctx, `CREATE FUNCTION pg_temp.sha256(bytea) RETURNS bytea AS $$ SELECT '\x00'::bytea $$ LANGUAGE sql`); err != nil {
				t.Skipf("cannot create temp functions here: %v", err)
			}
			if l.begin(tx, sessionctx.Sign(otherKey(), uuid.New(), true, time.Now())) {
				t.Error("a temp function shadowing sha256 made a forged signature pass")
			}
		})
	})

	t.Run("cannot become the owner", func(t *testing.T) {
		l.tx(func(tx pgx.Tx) {
			if _, err := tx.Exec(l.ctx, `SET LOCAL ROLE omnira`); err == nil {
				t.Error("SET ROLE to the owner succeeded")
			}
		})
	})
}

func TestTheModesOffMeasureAndEnforce(t *testing.T) {
	l := newCtxLab(t)
	user := uuid.New()

	t.Run("off: the legacy settings decide (what ran before the migration)", func(t *testing.T) {
		l.withMode("off")
		l.tx(func(tx pgx.Tx) {
			l.forge(tx, user, true)
			if u, sys := l.identity(tx); u == nil || *u != user || !sys {
				t.Errorf("mode off must keep trusting the settings: user=%v system=%v", u, sys)
			}
		})
	})

	t.Run("measure: the legacy settings decide and a divergence is reported", func(t *testing.T) {
		l.withMode("measure")
		cfg := l.app.Config().ConnConfig.Copy()
		var mu sync.Mutex
		var notices []string
		cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
			mu.Lock()
			notices = append(notices, n.Message)
			mu.Unlock()
		}
		conn, err := pgx.ConnectConfig(l.ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(l.ctx)
		tx, err := conn.Begin(l.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(l.ctx) }()
		// forged settings, no genuine context: the legacy value decides, and the divergence is reported
		if _, err := tx.Exec(l.ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
			t.Fatal(err)
		}
		var sys bool
		if err := tx.QueryRow(l.ctx, `SELECT is_system_admin()`).Scan(&sys); err != nil {
			t.Fatal(err)
		}
		if !sys {
			t.Error("mode measure must still let the legacy setting decide")
		}
		mu.Lock()
		defer mu.Unlock()
		found := false
		for _, n := range notices {
			if strings.Contains(n, "session_ctx divergence: is_system_admin legacy=true verified=false") {
				found = true
			}
		}
		if !found {
			t.Errorf("no divergence warning for a forged administrator flag; notices: %q", notices)
		}
	})

	t.Run("measure: a session that signs correctly reports nothing", func(t *testing.T) {
		l.withMode("measure")
		cfg := l.app.Config().ConnConfig.Copy()
		var mu sync.Mutex
		var notices []string
		cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
			mu.Lock()
			notices = append(notices, n.Message)
			mu.Unlock()
		}
		conn, err := pgx.ConnectConfig(l.ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(l.ctx)
		tx, err := conn.Begin(l.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(l.ctx) }()
		if err := sessionctx.Set(l.ctx, tx, user, true); err != nil {
			t.Fatal(err)
		}
		if u, sys := l.identity(tx); u == nil || *u != user || !sys {
			t.Errorf("user=%v system=%v", u, sys)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, n := range notices {
			if strings.Contains(n, "divergence") {
				t.Errorf("a correctly signed session reported a divergence: %s", n)
			}
		}
	})

	t.Run("enforce needs a key installed", func(t *testing.T) {
		tx, err := l.owner.Begin(l.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(l.ctx) }()
		if _, err := tx.Exec(l.ctx, `DELETE FROM session_ctx_keys`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(l.ctx, `SELECT ctx_set_mode('enforce')`); err == nil {
			t.Error("enforce was accepted without a key (every session would lose access)")
		}
	})

	t.Run("an unknown mode is refused", func(t *testing.T) {
		if _, err := l.owner.Exec(l.ctx, `SELECT ctx_set_mode('yolo')`); err == nil {
			t.Error("accepted an unknown mode")
		}
	})
}

func TestKeyRotationNeverLogsAnybodyOutAndRetiresTheOldKey(t *testing.T) {
	l := newCtxLab(t)
	l.withMode("enforce")
	user := uuid.New()
	// leave the database with the test key as the current one whatever happens
	t.Cleanup(func() { _, _ = l.owner.Exec(context.Background(), `SELECT ctx_install_key($1)`, l.key) })
	next := otherKey()
	third, _ := hex.DecodeString(strings.Repeat("d4", 32))
	accepts := func(key []byte) bool {
		ok := false
		l.tx(func(tx pgx.Tx) { ok = l.begin(tx, sessionctx.Sign(key, user, false, time.Now())) })
		return ok
	}
	install := func(key []byte) {
		if _, err := l.owner.Exec(l.ctx, `SELECT ctx_install_key($1)`, key); err != nil {
			t.Fatal(err)
		}
	}
	if !accepts(l.key) || accepts(next) {
		t.Fatal("precondition: only the installed key is accepted")
	}
	install(next)
	if !accepts(next) || !accepts(l.key) {
		t.Error("after a rotation both the new and the previous key must work (instances not restarted yet keep signing with the old one)")
	}
	install(third)
	if !accepts(third) || !accepts(next) || accepts(l.key) {
		t.Error("after a second rotation the key before the previous one must stop working")
	}
	if _, err := l.owner.Exec(l.ctx, `SELECT ctx_drop_previous_key()`); err != nil {
		t.Fatal(err)
	}
	if !accepts(third) || accepts(next) {
		t.Error("dropping the previous key must leave only the current one")
	}
	// installing a key that is already current changes nothing
	var res string
	if err := l.owner.QueryRow(l.ctx, `SELECT ctx_install_key($1)`, third).Scan(&res); err != nil || res != "unchanged" {
		t.Errorf("reinstalling the current key: %q %v", res, err)
	}
	if _, err := l.owner.Exec(l.ctx, `SELECT ctx_install_key($1)`, []byte("short")); err == nil {
		t.Error("a short key was accepted")
	}
}

func TestSessionsOpenedByTheApplicationAreVerifiedEndToEnd(t *testing.T) {
	l := newCtxLab(t)
	l.withMode("enforce")
	user := uuid.New()
	before := sessionctx.Rejections()
	check := func(system bool) {
		if err := WithTenantSession(l.ctx, l.app, user, system, func(sc context.Context) error {
			var u *uuid.UUID
			var sys bool
			if err := QuerierFromContext(sc, l.app).QueryRow(sc, `SELECT current_user_id(), is_system_admin()`).Scan(&u, &sys); err != nil {
				return err
			}
			if u == nil || *u != user || sys != system {
				t.Errorf("WithTenantSession(system=%v): the database sees user=%v system=%v", system, u, sys)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(false)
	check(true)
	if got := sessionctx.Rejections(); got != before {
		t.Errorf("the database refused %d context(s) the application signed", got-before)
	}
	// system helper used by every worker
	tenant := uuid.New()
	if err := WithSystemTenantSession(l.ctx, l.app, tenant, func(sc context.Context) error {
		var sys bool
		if err := QuerierFromContext(sc, l.app).QueryRow(sc, `SELECT is_system_admin()`).Scan(&sys); err != nil || !sys {
			t.Errorf("a system tenant session is not a system session: %v %v", sys, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestADelegatedContextCannotBeForgedOnlyGranted(t *testing.T) {
	l := newCtxLab(t)
	l.withMode("enforce")
	user, hub, tenant := uuid.New(), uuid.New(), uuid.New()
	l.tx(func(tx pgx.Tx) {
		l.begin(tx, sessionctx.Sign(l.key, user, false, time.Now()))
		// a user without any delegation asks for one: refused, and nothing is recorded
		var ok bool
		if err := tx.QueryRow(l.ctx, `SELECT lock_served_tenant($1, $2, $3)`, tenant, user, hub).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("a delegation nobody granted was locked")
		}
		var h, ten *uuid.UUID
		var present bool
		if err := tx.QueryRow(l.ctx, `SELECT acting_hub(), acting_tenant(), ctx_acting_present()`).Scan(&h, &ten, &present); err != nil {
			t.Fatal(err)
		}
		if h != nil || ten != nil || present {
			t.Errorf("an acting context exists without a delegation: hub=%v tenant=%v present=%v", h, ten, present)
		}
		// the legacy settings are not an acting context
		if _, err := tx.Exec(l.ctx, `SELECT set_config('app.acting_hub', $1, true), set_config('app.acting_tenant', $2, true)`, hub.String(), tenant.String()); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(l.ctx, `SELECT acting_hub(), acting_tenant(), ctx_acting_present()`).Scan(&h, &ten, &present); err != nil {
			t.Fatal(err)
		}
		if h != nil || ten != nil || present {
			t.Errorf("forged settings became an acting context: hub=%v tenant=%v present=%v", h, ten, present)
		}
		// nor can the application write one directly
		if _, err := tx.Exec(l.ctx, `SELECT ctx_set_acting($1, $2)`, hub, tenant); err == nil {
			t.Error("the application role called ctx_set_acting")
		}
	})
}
