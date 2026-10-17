package adapters_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnira/omnira/internal/conversationcontrol"
	"github.com/omnira/omnira/internal/external/adapters"
	"github.com/omnira/omnira/internal/external/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Codex H2 MEDIUM: the project-wide lock order of an external write is installation -> conversation -> routing state -> timer. The administration takes the installation
// (FOR UPDATE) and then the conversations it drains; the external write path rechecks the installation (FOR SHARE) FIRST and then locks the conversation. Racing them must
// neither deadlock nor leave a hold behind a revoked installation.
func TestInstallationLifecycleAndControlOperationsNeverDeadlock(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	const rounds = 30
	svc := conversationcontrol.Service{}
	for i := 0; i < rounds; i++ {
		app := e.install(admin, a, fmt.Sprintf("CRM %d", i), "conversation.claim")
		var convs []uuid.UUID
		for j := 0; j < 4; j++ {
			c, _ := e.conv(a, convOpts{})
			convs = append(convs, c)
		}
		p := &domain.Principal{CredentialID: app.Credential.ID, InstallationID: app.Installation.ID, TenantID: a, Scopes: []domain.Scope{domain.ScopeConversationClaim}}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var writeErr error
		var revokeCode int
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			writeErr = platformdb.WithSystemTenantSession(e.ctx, e.app, a, func(ctx context.Context) error {
				q := platformdb.QuerierFromContext(ctx, e.app)
				if err := adapters.RecheckPrincipal(ctx, q, p); err != nil {
					return err
				}
				for _, c := range convs {
					if _, _, err := svc.ClaimByApplication(ctx, q, a, c, app.Installation.ID, nil, conversationcontrol.Actor{Kind: conversationcontrol.ActorCRMApplication, ID: app.Installation.ID}); err != nil {
						return err
					}
				}
				return nil
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			revokeCode, _ = e.admin(admin, a, http.MethodPost, "/"+app.Installation.ID.String()+"/revoke", nil)
		}()
		close(start)
		wg.Wait()
		var pe *pgconn.PgError
		if errors.As(writeErr, &pe) && pe.Code == "40P01" {
			t.Fatalf("round %d: the external write deadlocked with the revocation: %v", i, writeErr)
		}
		if writeErr != nil && !errors.Is(writeErr, adapters.ErrInstallationInactive) {
			t.Fatalf("round %d: the write failed for a reason other than the revocation: %v", i, writeErr)
		}
		if revokeCode != http.StatusOK {
			t.Fatalf("round %d: the revocation answered %d (a deadlock victim would be a 500)", i, revokeCode)
		}
		var held int
		if err := e.seed.QueryRow(e.ctx, `SELECT count(*) FROM conversations WHERE control_holder_id = $1`, app.Installation.ID).Scan(&held); err != nil || held != 0 {
			t.Fatalf("round %d: %d conversations stay held by a revoked installation (%v)", i, held, err)
		}
	}
}
