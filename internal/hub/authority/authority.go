// Package authority holds the one in-transaction proof that a person is, right now, an active administrator of an active hub.
// The Access panel (ADR-0039) and the work pools (ADR-0038 phase 4) both write through a system session after this proof, so the
// rule lives in one place.
package authority

import (
	"context"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// LockHubAdmin pins the rows the answer depends on (hub, hub membership, user) until the transaction ends, then asks the database:
// is this person an administrator of this hub, is the hub active and is the person's account active. Revoking the admin, pausing the
// hub or deactivating the account each UPDATE one of the pinned rows, so the change either commits before the answer or waits until
// the write that follows is done: there is no moment where something is written on the authority of someone who just lost it.
// One statement per table, always in the same order the administrative paths take them (hub, membership, user).
func LockHubAdmin(ctx context.Context, q platformdb.Querier, hub, person uuid.UUID) (bool, error) {
	for _, pin := range []struct {
		sql  string
		args []any
	}{
		{`SELECT 1 FROM service_hubs WHERE id = $1 FOR SHARE`, []any{hub}},
		{`SELECT 1 FROM hub_memberships WHERE hub_id = $1 AND user_id = $2 FOR SHARE`, []any{hub, person}},
		{`SELECT 1 FROM users WHERE id = $1 FOR SHARE`, []any{person}},
	} {
		rows, err := q.Query(ctx, pin.sql, pin.args...)
		if err != nil {
			return false, err
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}
	}
	var ok bool
	err := q.QueryRow(ctx, `SELECT is_hub_admin($1, $2) AND EXISTS (SELECT 1 FROM service_hubs WHERE id = $1 AND status = 'active')
	                              AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')`, hub, person).Scan(&ok)
	return ok, err
}
