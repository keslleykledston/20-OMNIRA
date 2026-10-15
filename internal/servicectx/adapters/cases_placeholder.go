package adapters

import "net/http"

// CasesHandler is filled in by the cases/tasks phase (ADR-0049 phase 3); until then the module serves sessions and catalogs only.
type CasesHandler struct{}

func (c *CasesHandler) Routes(Registrar, func(http.HandlerFunc) http.Handler, func(string, http.Handler) http.Handler) {
}
