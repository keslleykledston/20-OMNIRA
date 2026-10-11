// Package sessionctx signs the identity of a database session and has the database verify it (ADR-0042). It depends on nothing else of the application so
// that the session helper, the test helpers and the tools can all use it.
package sessionctx

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ADR-0042: the identity of a database session is not trusted from settings the application role can write; it is SIGNED here and VERIFIED by the database
// (ctx_begin, migration 000118). The key comes from the environment of the process (hex, at least 32 bytes) and is installed in the database by the migrator
// (tools/migrate-sql.sh) from the same variable. Without a key the application keeps setting only the legacy settings (database mode "off").
// Querier is the part of a pgx transaction or pool this package needs.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// KeyEnv is the environment variable holding the signing key.
const KeyEnv = "OMNIRA_SESSION_CTX_KEY"

var (
	keyOnce sync.Once
	keyVal  []byte
	keyErr  error

	// rejected counts contexts the database refused (wrong key, stale clock...). In mode enforce every one of them is a session without access.
	rejected   atomic.Int64
	lastWarnAt atomic.Int64
)

// Key returns the signing key of this process: nil when none is configured, an error when the variable is set but not a valid key
// (a misconfiguration must be loud, never a silent downgrade to unsigned sessions).
func Key() ([]byte, error) {
	keyOnce.Do(func() { keyVal, keyErr = parseSessionKey(os.Getenv(KeyEnv)) })
	return keyVal, keyErr
}

func parseSessionKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be hex: %w", KeyEnv, err)
	}
	if len(b) < 32 {
		return nil, fmt.Errorf("%s must have at least 32 bytes (64 hex characters)", KeyEnv)
	}
	return b, nil
}

// Rejections is how many contexts the database refused since the process started.
func Rejections() int64 { return rejected.Load() }

// Sign builds "b1|<user or empty>|<0|1>|<backend pid>|<issued at>|<hmac-sha256 hex>", the exact format ctx_begin verifies. The pid ties the context to the connection it is
// sent on: a copy captured from another session is useless.
func Sign(key []byte, userID uuid.UUID, system bool, pid uint32, at time.Time) string {
	user := ""
	if userID != uuid.Nil {
		user = userID.String()
	}
	flag := "0"
	if system {
		flag = "1"
	}
	payload := fmt.Sprintf("b1|%s|%s|%d|%d", user, flag, pid, at.Unix())
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

// Set is the ONE place that establishes who a transaction acts as. It must be the first thing run in the transaction (the verified context is valid
// for the transaction that recorded it). It sets the legacy settings (still read in database modes off and measure) and, when a key is configured, has the
// database verify and record the signed context in the same round trip.
func Set(ctx context.Context, q Querier, userID uuid.UUID, isSystemAdmin bool) error {
	key, err := Key()
	if err != nil {
		return err
	}
	if key == nil {
		if _, err := q.Exec(ctx, `SELECT set_config('app.current_user_id', $1, true)`, userID.String()); err != nil {
			return err
		}
		if isSystemAdmin {
			if _, err := q.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
				return err
			}
		}
		return nil
	}
	pid, found := backendPID(q)
	if !found {
		return errors.New("session context: cannot tell which database backend this transaction runs on")
	}
	var ok bool
	err = q.QueryRow(ctx,
		`SELECT ctx_begin($1), set_config('app.current_user_id', $2, true), CASE WHEN $3::boolean THEN set_config('app.is_system_admin', 'true', true) END`,
		Sign(key, userID, isSystemAdmin, pid, time.Now()), userID.String(), isSystemAdmin,
	).Scan(&ok, new(string), new(*string))
	if err != nil {
		return err
	}
	if !ok {
		noteRejected()
	}
	return nil
}

// backendPID is the PostgreSQL backend a transaction (or connection) runs on. pgx transactions expose their connection; the second form lets a test double say it.
func backendPID(q Querier) (uint32, bool) {
	switch c := q.(type) {
	case interface{ Conn() *pgx.Conn }:
		if cc := c.Conn(); cc != nil && cc.PgConn() != nil {
			return cc.PgConn().PID(), true
		}
	case interface{ PID() uint32 }:
		return c.PID(), true
	}
	return 0, false
}

func noteRejected() {
	rejected.Add(1)
	now := time.Now().Unix()
	if last := lastWarnAt.Load(); now-last >= 30 && lastWarnAt.CompareAndSwap(last, now) {
		log.Printf("session context: the database REFUSED a signed context (key mismatch with the migrator, or clock skew over 60 s); in mode enforce these sessions have no access")
	}
}
