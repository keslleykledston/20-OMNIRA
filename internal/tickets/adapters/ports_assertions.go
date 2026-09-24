package adapters

import "github.com/omnira/omnira/internal/tickets/ports"

// Compile-time proof that these adapters satisfy the ports the
// PRODUCT.6-K2 application service depends on (internal/tickets/ports).
var (
	_ ports.AttemptStore             = (*AttemptStore)(nil)
	_ ports.LocalTicketStore         = (*LocalTicketStore)(nil)
	_ ports.ConversationAuthorizer   = (*ConversationAuthorizer)(nil)
	_ ports.CompanyDirectory         = (*K3GCompanyDirectory)(nil)
	_ ports.TicketingRuntimeResolver = (*K3GTicketingRuntimeResolver)(nil)
)
