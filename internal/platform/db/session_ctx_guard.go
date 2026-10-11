package db

import (
	"context"
	"fmt"
	"log"

	"github.com/omnira/omnira/internal/platform/sessionctx"
)

// CheckSessionContext tells the operator how the database treats session identities (ADR-0042) and, when requireEnforce is set (OMNIRA_SESSION_CTX_REQUIRE_ENFORCE=true), refuses to let the
// process run unless the database trusts ONLY signed contexts and this process has the key to sign them. Without the flag it logs the mode and warns when it is not enforce: a staged
// rollout (off -> measure -> enforce) is legitimate, a forgotten one is not, and the process cannot tell the two apart by itself.
func CheckSessionContext(ctx context.Context, q Querier, requireEnforce bool) (string, error) {
	key, keyErr := sessionctx.Key()
	if keyErr != nil {
		return "", keyErr
	}
	var mode string
	if err := q.QueryRow(ctx, `SELECT public.ctx_mode_public()`).Scan(&mode); err != nil {
		// a database before migration 000118 has no such function: sessions are the legacy ones
		if requireEnforce {
			return "", fmt.Errorf("session context: the database does not report a mode (migration 000118 not applied?): %w", err)
		}
		log.Printf("session context: database without migration 000118; identities are the legacy settings")
		return "legacy", nil
	}
	if requireEnforce {
		if mode != "enforce" {
			return mode, fmt.Errorf("session context: the database mode is %q, OMNIRA_SESSION_CTX_REQUIRE_ENFORCE=true needs \"enforce\"", mode)
		}
		if key == nil {
			return mode, fmt.Errorf("session context: the database is in enforce mode but this process has no %s: every session would have no access", sessionctx.KeyEnv)
		}
	}
	state := "key configured"
	if key == nil {
		state = "NO key configured"
	}
	if mode == "enforce" {
		log.Printf("session context: mode enforce, %s", state)
	} else {
		log.Printf("WARNING: session context: mode %s (%s): session identities are NOT yet verified by the database; finish the rollout (scripts/session-ctx.sh) and set OMNIRA_SESSION_CTX_REQUIRE_ENFORCE=true", mode, state)
	}
	return mode, nil
}
