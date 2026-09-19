package adapters

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// AuthorizationMiddleware — middleware que valida acesso a um tenant.
// Extrai Principal do context, autoriza acesso ao tenant, injeta TenantContext.
// Retorna 403 se não autorizado, 404 se tenant não encontrado.
func AuthorizationMiddleware(authzSvc *application.AuthorizationService) func(http.Handler) http.Handler {
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

			// Autorizar acesso
			tenantContext, err := authzSvc.AuthorizeAccessToTenant(r.Context(), tenantUUID, principal.UserID)
			if err != nil {
				if errors.Is(err, errors.New("tenant not found")) {
					http.Error(w, "tenant not found", http.StatusNotFound)
				} else if errors.Is(err, errors.New("access denied: no active membership")) {
					http.Error(w, "forbidden", http.StatusForbidden)
				} else if errors.Is(err, errors.New("tenant is not active")) {
					http.Error(w, "tenant is not active", http.StatusForbidden)
				} else {
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
				return
			}

			// Injetar TenantContext no context
			ctx := domain.WithTenantContext(r.Context(), tenantContext)
			r = r.WithContext(ctx)

			next.ServeHTTP(w, r)
		})
	}
}
