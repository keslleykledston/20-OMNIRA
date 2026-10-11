package db

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// begin hands a context to ctx_begin and says whether the database accepted it. In enforce a refusal is an ERROR, so the call runs inside a savepoint: a refused context must not
// poison the rest of the test's transaction.
func (l *ctxLab) begin(tx pgx.Tx, raw string) bool {
	l.t.Helper()
	ok, _ := l.beginErr(tx, raw)
	return ok
}

func (l *ctxLab) beginErr(tx pgx.Tx, raw string) (bool, error) {
	l.t.Helper()
	if _, err := tx.Exec(l.ctx, `SAVEPOINT ctx_try`); err != nil {
		l.t.Fatal(err)
	}
	var ok bool
	if err := tx.QueryRow(l.ctx, `SELECT ctx_begin($1)`, raw).Scan(&ok); err != nil {
		if _, rerr := tx.Exec(l.ctx, `ROLLBACK TO SAVEPOINT ctx_try`); rerr != nil {
			l.t.Fatal(rerr)
		}
		return false, err
	}
	if _, err := tx.Exec(l.ctx, `RELEASE SAVEPOINT ctx_try`); err != nil {
		l.t.Fatal(err)
	}
	return ok, nil
}

func (l *ctxLab) forge(tx pgx.Tx, user uuid.UUID, system bool) {
	l.t.Helper()
	if _, err := tx.Exec(l.ctx, `SELECT set_config('app.current_user_id', $1, true), set_config('app.is_system_admin', $2, true)`, user.String(), fmt.Sprint(system)); err != nil {
		l.t.Fatal(err)
	}
}

// signFor asks the database for THIS transaction's challenge and answers it with a token signed with key (what the application's sessionctx.Set does).
func (l *ctxLab) signFor(tx pgx.Tx, key []byte, user uuid.UUID, system bool) string {
	l.t.Helper()
	var nonce string
	if err := tx.QueryRow(l.ctx, `SELECT ctx_challenge()`).Scan(&nonce); err != nil {
		l.t.Fatal(err)
	}
	return sessionctx.Sign(key, user, system, nonce)
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
			if l.begin(tx, l.signFor(tx, otherKey(), user, true)) {
				t.Fatal("a context signed with a key the database does not know was accepted")
			}
			if u, sys := l.identity(tx); u != nil || sys {
				t.Fatalf("identity from a refused context: user=%v system=%v", u, sys)
			}
		})
	})

	t.Run("a genuine context is believed, and only for what it says", func(t *testing.T) {
		l.tx(func(tx pgx.Tx) {
			if !l.begin(tx, l.signFor(tx, l.key, user, false)) {
				t.Fatal("a genuine context was refused")
			}
			l.forge(tx, uuid.New(), true) // forging on top of a genuine, non-system context changes nothing
			if u, sys := l.identity(tx); u == nil || *u != user || sys {
				t.Fatalf("want user %s, not system; got user=%v system=%v", user, u, sys)
			}
		})
		l.tx(func(tx pgx.Tx) {
			if !l.begin(tx, l.signFor(tx, l.key, uuid.Nil, true)) {
				t.Fatal("a genuine system context was refused")
			}
			if u, sys := l.identity(tx); u == nil || *u != uuid.Nil || !sys {
				t.Fatalf("want system with the nil user (as the legacy setting said); got user=%v system=%v", u, sys)
			}
		})
	})

	t.Run("tampering with a signed context breaks it", func(t *testing.T) {
		cases := map[string]func(parts []string, good string) string{
			"system flag flipped":  func(p []string, _ string) string { p[2] = "1"; return strings.Join(p, "|") },
			"another user":         func(p []string, _ string) string { p[1] = uuid.NewString(); return strings.Join(p, "|") },
			"another nonce":        func(p []string, _ string) string { p[3] = strings.Repeat("a", 32); return strings.Join(p, "|") },
			"signature truncated":  func(_ []string, g string) string { return g[:len(g)-2] },
			"signature uppercased": func(p []string, _ string) string { p[4] = strings.ToUpper(p[4]); return strings.Join(p, "|") },
			"extra field":          func(_ []string, g string) string { return g + "|x" },
			"empty":                func(_ []string, _ string) string { return "" },
			"garbage":              func(_ []string, _ string) string { return "b1|||" },
			"wrong tag":            func(_ []string, g string) string { return "b2" + g[2:] },
		}
		for name, mutate := range cases {
			l.tx(func(tx pgx.Tx) {
				good := l.signFor(tx, l.key, user, false)
				raw := mutate(strings.Split(good, "|"), good)
				if l.begin(tx, raw) {
					t.Errorf("%s: a tampered context was accepted", name)
				}
				if u, sys := l.identity(tx); u != nil || sys {
					t.Errorf("%s: identity from a tampered context: user=%v system=%v", name, u, sys)
				}
			})
		}
	})

	t.Run("a token is good for the transaction that was challenged, and nowhere else", func(t *testing.T) {
		var captured string
		l.tx(func(tx pgx.Tx) { captured = l.signFor(tx, l.key, uuid.Nil, true) }) // a genuine system token, signed and never used (the request died, say)
		l.tx(func(tx pgx.Tx) {
			if l.begin(tx, captured) {
				t.Error("a token signed over another transaction's nonce was accepted")
			}
			if _, sys := l.identity(tx); sys {
				t.Error("replaying a captured system token made the session an administrator")
			}
		})
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
		for _, commit := range []bool{true, false} { // a request that finished and one that rolled back: the captured token is dead either way
			tx, err := pool.Begin(l.ctx)
			if err != nil {
				t.Fatal(err)
			}
			tok := l.signFor(tx, l.key, uuid.Nil, true)
			if !l.begin(tx, tok) {
				t.Fatal("the genuine token was refused")
			}
			if commit {
				_ = tx.Commit(l.ctx)
			} else {
				_ = tx.Rollback(l.ctx)
			}
			again, err := pool.Begin(l.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if l.begin(again, tok) {
				t.Errorf("commit=%v: the same token was accepted in a later transaction of the same backend", commit)
			}
			if _, sys := l.identity(again); sys {
				t.Errorf("commit=%v: replay made the session an administrator", commit)
			}
			_ = again.Rollback(l.ctx)
		}
	})

	t.Run("a challenge that was answered by nobody does not linger into the next transaction", func(t *testing.T) {
		one, err := pgxpool.ParseConfig(l.app.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		one.MaxConns = 1
		pool, err := pgxpool.NewWithConfig(l.ctx, one)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		first, err := pool.Begin(l.ctx)
		if err != nil {
			t.Fatal(err)
		}
		stale := l.signFor(first, l.key, uuid.Nil, true) // challenged and signed, never presented
		if err := first.Commit(l.ctx); err != nil {      // the registry row with that nonce is now committed
			t.Fatal(err)
		}
		next, err := pool.Begin(l.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = next.Rollback(l.ctx) }()
		if l.begin(next, stale) {
			t.Error("a token for the registry row of an earlier transaction was accepted")
		}
	})

	t.Run("a holder of the key still needs the database's challenge", func(t *testing.T) {
		l.tx(func(tx pgx.Tx) {
			if l.begin(tx, signRaw(l.key, "b1|"+user.String()+"|1|"+strings.Repeat("0", 32))) {
				t.Error("a token over a nonce the database never issued was accepted")
			}
		})
		l.tx(func(tx pgx.Tx) { // no challenge at all
			if l.begin(tx, signRaw(l.key, "b1|"+user.String()+"|0|"+strings.Repeat("a", 32))) {
				t.Error("a token without any challenge in this transaction was accepted")
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
			l.begin(tx, l.signFor(tx, l.key, uuid.Nil, true))
			if n := count(tx); n != 1 {
				t.Errorf("a genuine system context read %d tenant(s), want 1", n)
			}
		})
	})
}

// signRaw signs ANY payload with the key, the way a holder of the key could: the database must still refuse what is not exactly the protocol.
func signRaw(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

func TestAHolderOfTheKeyCannotSendAnythingButTheProtocol(t *testing.T) {
	l := newCtxLab(t)
	l.withMode("enforce")
	user := uuid.New()
	l.tx(func(tx pgx.Tx) {
		var nonce string
		if err := tx.QueryRow(l.ctx, `SELECT ctx_challenge()`).Scan(&nonce); err != nil {
			t.Fatal(err)
		}
		cases := map[string]string{
			"another tag":         fmt.Sprintf("b2|%s|0|%s", user, nonce),
			"an extra field":      fmt.Sprintf("b1|%s|0|%s|x", user, nonce),
			"a missing field":     fmt.Sprintf("b1|%s|0", user),
			"a system flag of 2":  fmt.Sprintf("b1|%s|2|%s", user, nonce),
			"a user that is not":  fmt.Sprintf("b1|not-a-uuid|0|%s", nonce),
			"an empty user":       fmt.Sprintf("b1||0|%s", nonce),
			"a short nonce":       fmt.Sprintf("b1|%s|0|%s", user, nonce[:30]),
			"an uppercased nonce": fmt.Sprintf("b1|%s|0|%s", user, strings.ToUpper(nonce)),
		}
		for name, payload := range cases {
			if l.begin(tx, signRaw(l.key, payload)) {
				t.Errorf("%s: accepted", name)
			}
		}
		if !l.begin(tx, signRaw(l.key, fmt.Sprintf("b1|%s|0|%s", user, nonce))) {
			t.Error("the exact protocol, signed with the key, was refused")
		}
	})
}

func TestAContextIsAcceptedOncePerTransactionAndARefusalIsLoudInEnforce(t *testing.T) {
	l := newCtxLab(t)
	user, admin := uuid.New(), uuid.New()

	t.Run("a second genuine context cannot change who the session is", func(t *testing.T) {
		l.withMode("enforce")
		l.tx(func(tx pgx.Tx) {
			if !l.begin(tx, l.signFor(tx, l.key, user, false)) {
				t.Fatal("the first context was refused")
			}
			// a perfectly valid context for this very backend (a fresh challenge, a genuine signature): refused, the identity stays
			if l.begin(tx, l.signFor(tx, l.key, admin, true)) { // a fresh challenge, a perfectly valid signature
				t.Fatal("a second context in the same transaction was accepted")
			}
			if u, sys := l.identity(tx); u == nil || *u != user || sys {
				t.Fatalf("the session changed identity: user=%v system=%v", u, sys)
			}
		})
	})

	t.Run("enforce: a refused context is an error (the request must not run)", func(t *testing.T) {
		l.withMode("enforce")
		l.tx(func(tx pgx.Tx) {
			ok, err := l.beginErr(tx, l.signFor(tx, otherKey(), user, true))
			if ok || err == nil {
				t.Fatalf("a refused context must raise in enforce: ok=%v err=%v", ok, err)
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "28000" {
				t.Errorf("want SQLSTATE 28000, got %v", err)
			}
		})
	})

	t.Run("off and measure: a refused context is a false, never an error", func(t *testing.T) {
		for _, mode := range []string{"off", "measure"} {
			l.withMode(mode)
			l.tx(func(tx pgx.Tx) {
				ok, err := l.beginErr(tx, l.signFor(tx, otherKey(), user, true))
				if ok || err != nil {
					t.Errorf("%s: ok=%v err=%v; want false and no error (the legacy settings still decide)", mode, ok, err)
				}
			})
		}
	})

	t.Run("the application does not run a session the database refused (enforce)", func(t *testing.T) {
		l.withMode("enforce")
		// a pool whose process signs with a key the database does not know
		restore := sessionctx.UseKeyForTesting(otherKey())
		defer restore()
		ran := false
		err := WithTenantSession(l.ctx, l.app, user, false, func(context.Context) error { ran = true; return nil })
		if err == nil || ran {
			t.Errorf("a session whose context was refused ran anyway: err=%v ran=%v", err, ran)
		}
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
			if _, err := tx.Exec(l.ctx, `INSERT INTO session_ctx_verified (pid, xid, uid, sys) VALUES (pg_backend_pid(), pg_current_xact_id(), gen_random_uuid(), true)`); err == nil {
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
			"ctx_set_acting(uuid,uuid)", "ctx_measure(text,text,text)", "ctx_verified(text)", "ctx_refused()",
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
			if l.begin(tx, l.signFor(tx, otherKey(), uuid.New(), true)) {
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
		l.tx(func(tx pgx.Tx) { ok = l.begin(tx, l.signFor(tx, key, user, false)) })
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
		l.begin(tx, l.signFor(tx, l.key, user, false))
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
