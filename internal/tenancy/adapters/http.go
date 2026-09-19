package adapters

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// trackedResponseWriter — embrulha http.ResponseWriter para registrar se
// alguém já escreveu status/corpo. Necessário porque o handler downstream
// roda dentro de platformdb.WithTenantSession: se ele já respondeu (ex.: um
// erro de negócio dentro da própria query) e DEPOIS o Commit da transação
// falhar (a transação já estava abortada pela query que falhou), o
// middleware não pode tentar escrever outro http.Error por cima — isso
// dispara "superfluous response.WriteHeader call" e corrompe a resposta já
// enviada ao cliente.
type trackedResponseWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *trackedResponseWriter) WriteHeader(code int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *trackedResponseWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

// AuthorizationMiddleware — middleware que valida acesso a um tenant.
// Extrai Principal do context, abre uma sessão de banco com
// app.current_user_id setado (necessária para a própria checagem de
// autorização funcionar sob RLS — ver internal/platform/db), autoriza
// acesso ao tenant e injeta TenantContext. A sessão de banco permanece
// aberta durante todo next.ServeHTTP, para que o handler downstream also
// rode sob o mesmo app.current_user_id.
// Retorna 403 se não autorizado, 404 se tenant não encontrado.
func AuthorizationMiddleware(pool *pgxpool.Pool, authzSvc *application.AuthorizationService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extrair Principal do context (injetado pelo authn middleware)
			principal, err := authn.FromContext(r.Context())
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			// Extrair tenant_id da URL (exemplo: /api/v1/tenants/{tenant_id})
			tenantID := r.PathValue("tenant_id")
			if tenantID == "" {
				http.Error(w, "tenant_id required", http.StatusBadRequest)
				return
			}

			tenantUUID, err := uuid.Parse(tenantID)
			if err != nil {
				http.Error(w, "invalid tenant_id", http.StatusBadRequest)
				return
			}

			tw := &trackedResponseWriter{ResponseWriter: w}
			sessionErr := platformdb.WithTenantSession(r.Context(), pool, principal.UserID, false, func(ctx context.Context) error {
				// Autorizar acesso (já roda com app.current_user_id setado)
				tenantContext, err := authzSvc.AuthorizeAccessToTenant(ctx, tenantUUID, principal.UserID)
				if err != nil {
					writeAuthorizationError(tw, err)
					return errHandled
				}

				// Injetar TenantContext no context e servir o handler
				// downstream dentro da mesma sessão/transação de banco.
				ctx = domain.WithTenantContext(ctx, tenantContext)
				next.ServeHTTP(tw, r.WithContext(ctx))
				return nil
			})

			if sessionErr != nil && !errors.Is(sessionErr, errHandled) {
				if tw.wrote {
					// O handler downstream já respondeu; o erro veio do
					// Commit da transação (provavelmente abortada por uma
					// query que já causou a resposta de erro). Não há mais
					// nada seguro a escrever na resposta — só registrar.
					log.Printf("tenancy: erro pós-resposta ao commitar sessão: %v", sessionErr)
					return
				}
				http.Error(tw, "internal server error", http.StatusInternalServerError)
			}
		})
	}
}

// UserSessionMiddleware — abre uma sessão de banco (app.current_user_id
// setado) a partir só do Principal autenticado, sem exigir/validar um
// tenant_id na URL. Usado por rotas "cross-tenant" da perspectiva do
// usuário, como GET /api/v1/tenants (lista os tenants do próprio usuário) —
// RLS por si só já restringe cada query ao que este usuário pode ver.
func UserSessionMiddleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := authn.FromContext(r.Context())
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			tw := &trackedResponseWriter{ResponseWriter: w}
			sessionErr := platformdb.WithTenantSession(r.Context(), pool, principal.UserID, false, func(ctx context.Context) error {
				next.ServeHTTP(tw, r.WithContext(ctx))
				return nil
			})
			if sessionErr != nil {
				if tw.wrote {
					log.Printf("tenancy: erro pós-resposta ao commitar sessão: %v", sessionErr)
					return
				}
				http.Error(tw, "internal server error", http.StatusInternalServerError)
			}
		})
	}
}

// errHandled — sentinel usado para sinalizar a WithTenantSession que o erro
// já foi escrito na resposta HTTP (via writeAuthorizationError) e não deve
// gerar mais uma escrita de erro genérica — apenas abortar o commit da
// transação (o defer tx.Rollback cuida disso).
var errHandled = errors.New("handled")

func writeAuthorizationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrTenantNotFound):
		http.Error(w, "tenant not found", http.StatusNotFound)
	case errors.Is(err, application.ErrNoActiveMembership):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, application.ErrTenantNotActive):
		http.Error(w, "tenant is not active", http.StatusForbidden)
	case errors.Is(err, application.ErrInvalidTenantID), errors.Is(err, application.ErrInvalidActorID):
		http.Error(w, "invalid request", http.StatusBadRequest)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
