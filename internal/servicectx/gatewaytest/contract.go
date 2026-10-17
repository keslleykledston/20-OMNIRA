package gatewaytest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// Contract is what a real ERP adapter must satisfy before OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED may be turned on for it (docs/service-context/16). It runs against a
// SANDBOX of the provider (never a customer's real ERP): it creates service orders there. It checks the behaviours the module's durability depends on, not the
// provider's wire format: a created order comes back as a usable snapshot, can be read again, an unknown id is "not found" (not a failure of the call), every error is
// a classified GatewayError with a closed class, a call whose context ended is UNKNOWN (never "rejected": the write may be on its way), and, when the adapter can find a
// record again by correlation key, that works and is exact.
//
// Check returns the problems found, empty when the adapter conforms; customerRef is a customer the sandbox accepts ("" = none).
func Check(g ports.ServiceOrderGateway, customerRef string) (problems []string) {
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	corr := func(n string) string { return "contract-" + n + "-" + time.Now().UTC().Format("150405.000000") }

	if s := g.System(); !domain.IsToken(s) {
		bad("System() = %q, want a lower-case token", s)
	}

	key := corr("create")
	snap, err := g.CreateServiceOrder(ctx, ports.CreateOrderRequest{CorrelationKey: key, Title: "Contrato de teste do adaptador", Description: "criada pelo teste de contrato", CustomerRef: customerRef})
	if err != nil {
		bad("create in the sandbox failed: %v", err)
	} else if clean, cerr := snap.Clean(); cerr != nil {
		bad("a created order is not a usable snapshot: %v", cerr)
	} else {
		if clean.ExternalType != domain.ExternalTypeServiceOrder {
			bad("a created order must be a %q, got %q", domain.ExternalTypeServiceOrder, clean.ExternalType)
		}
		if clean.CorrelationKey != "" && clean.CorrelationKey != key {
			bad("the answer carries another request's correlation key: %q", clean.CorrelationKey)
		}
		if again, err := g.Get(ctx, clean.ExternalType, clean.ExternalID); err != nil {
			bad("the created order cannot be read back: %v", err)
		} else {
			if again.ExternalID != clean.ExternalID || again.ExternalType != clean.ExternalType {
				bad("read back %s/%s, asked %s/%s", again.ExternalType, again.ExternalID, clean.ExternalType, clean.ExternalID)
			}
			if _, err := again.Clean(); err != nil {
				bad("the read is not a usable snapshot: %v", err)
			}
		}
		if finder, ok := g.(ports.CorrelationLookup); ok {
			if got, err := finder.FindByCorrelation(ctx, key); err != nil {
				bad("find by correlation failed: %v", err)
			} else {
				if got.ExternalID != clean.ExternalID {
					bad("found order %s by correlation, created %s", got.ExternalID, clean.ExternalID)
				}
				if got.CorrelationKey != "" && got.CorrelationKey != key {
					bad("found a record that carries another key: %q", got.CorrelationKey)
				}
			}
			_, err := finder.FindByCorrelation(ctx, corr("never-created"))
			problems = append(problems, expectKind("an unknown correlation key", err, ports.GatewayNotFound)...)
		}
	}

	_, err = g.Get(ctx, domain.ExternalTypeServiceOrder, "id-que-nao-existe-"+corr("x"))
	problems = append(problems, expectKind("an id the ERP does not have", err, ports.GatewayNotFound)...)

	ended, stop := context.WithCancel(context.Background())
	stop()
	if got, err := g.CreateServiceOrder(ended, ports.CreateOrderRequest{CorrelationKey: corr("cancelled"), Title: "cancelada", CustomerRef: customerRef}); err == nil {
		bad("a create with an ended context answered %+v instead of failing", got)
	} else if kind, class := ports.KindOf(err); kind != ports.GatewayUnknown {
		bad("a create with an ended context was reported as %q (class %q): only UNKNOWN is safe, the write may be on its way and a rejection would free the session to ask again and duplicate the order", kind, class)
	}
	return problems
}

// RunContract fails t for every problem Check finds.
func RunContract(t *testing.T, g ports.ServiceOrderGateway, customerRef string) {
	t.Helper()
	for _, p := range Check(g, customerRef) {
		t.Error(p)
	}
}

// expectKind: err must be a classified gateway error of the given kind with a closed class token.
func expectKind(what string, err error, want ports.GatewayErrorKind) (problems []string) {
	if err == nil {
		return []string{fmt.Sprintf("%s: want a %q error, got success", what, want)}
	}
	var ge *ports.GatewayError
	if !errors.As(err, &ge) {
		return []string{fmt.Sprintf("%s: want a classified GatewayError of kind %q, got %T: %v (the module treats an unclassified error as UNKNOWN)", what, want, err, err)}
	}
	if ge.Kind != want {
		problems = append(problems, fmt.Sprintf("%s: kind = %q, want %q", what, ge.Kind, want))
	}
	if strings.TrimSpace(ge.Class) == "" || !domain.IsToken(ge.Class) {
		problems = append(problems, fmt.Sprintf("%s: class %q is not a closed lower-case token (a class goes to the history: never provider text)", what, ge.Class))
	}
	return problems
}
