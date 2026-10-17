package adapters

import (
	"context"

	"github.com/omnira/omnira/internal/external/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// RecheckPrincipal is the second look of every external MUTATION, taken inside the transaction that writes (ADR-0048 §3, the pattern of
// ADR-0037 for the Hub). external_recheck (migration 137) locks the installation row and the credential row FOR SHARE until the
// transaction ends and says whether both are usable now: revoking or suspending takes a conflicting row lock, so it either waits for this
// write to commit or happens first and this write is refused. Authentication, done earlier outside the transaction, is only a pre-check.
func RecheckPrincipal(ctx context.Context, q platformdb.Querier, p *domain.Principal) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT external_recheck($1, $2, $3)`, p.TenantID, p.InstallationID, p.CredentialID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrInstallationInactive
	}
	return nil
}
