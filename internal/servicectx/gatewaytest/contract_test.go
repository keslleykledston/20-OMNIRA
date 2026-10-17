package gatewaytest_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/gatewaytest"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

func faithful(t *testing.T, lookup bool) ports.ServiceOrderGateway {
	t.Helper()
	f := gatewaytest.New()
	f.Lookup = lookup
	g, err := gatewaytest.Resolver{F: f}.Resolve(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// The suite passes for a faithful adapter (the in-memory fake, with and without the correlation lookup) ...
func TestTheContractPassesForAFaithfulAdapter(t *testing.T) {
	t.Run("with correlation lookup", func(t *testing.T) { gatewaytest.RunContract(t, faithful(t, true), "") })
	t.Run("without correlation lookup (like the K3G)", func(t *testing.T) { gatewaytest.RunContract(t, faithful(t, false), "") })
}

// ... and it DISCRIMINATES: each way an adapter can break the module's durability is reported. A suite that cannot fail proves nothing.
type broken struct {
	ports.ServiceOrderGateway
	system        string
	createErr     func(error) error
	getNotFoundAs ports.GatewayErrorKind
	rawNotFound   bool
	emptyID       bool
	wrongType     bool
	otherKey      bool
	ignoreCtx     bool
}

func (b broken) System() string {
	if b.system != "" {
		return b.system
	}
	return b.ServiceOrderGateway.System()
}

func (b broken) CreateServiceOrder(ctx context.Context, r ports.CreateOrderRequest) (*domain.Snapshot, error) {
	if b.ignoreCtx {
		ctx = context.Background() // an adapter that calls out even though the context ended
	}
	s, err := b.ServiceOrderGateway.CreateServiceOrder(ctx, r)
	if err != nil {
		if b.createErr != nil {
			return nil, b.createErr(err)
		}
		return nil, err
	}
	c := *s
	if b.emptyID {
		c.ExternalID = ""
	}
	if b.wrongType {
		c.ExternalType = "customer"
	}
	if b.otherKey {
		c.CorrelationKey = "another-request"
	}
	return &c, nil
}

func (b broken) Get(ctx context.Context, t, id string) (*domain.Snapshot, error) {
	s, err := b.ServiceOrderGateway.Get(ctx, t, id)
	if err != nil && b.getNotFoundAs != "" {
		return nil, &ports.GatewayError{Kind: b.getNotFoundAs, Class: "not_found"}
	}
	if err != nil && b.rawNotFound {
		return nil, fmt.Errorf("404 from the provider") // an unclassified error (not wrapping a GatewayError)
	}
	return s, err
}

func TestTheContractReportsEachWayAnAdapterCanBreakTheModulesDurability(t *testing.T) {
	cases := map[string]struct {
		adapter broken
		want    string // a fragment of the problem that must be reported
	}{
		"a system name that is not a token":                    {broken{system: "Meu ERP!"}, "System()"},
		"a not-found reported as a rejection":                  {broken{getNotFoundAs: ports.GatewayRejected}, "kind"},
		"a not-found that is an unclassified raw error":        {broken{rawNotFound: true}, "classified GatewayError"},
		"an order with no id":                                  {broken{emptyID: true}, "usable snapshot"},
		"an order that is not a service order":                 {broken{wrongType: true}, "must be a"},
		"an answer carrying another request's correlation key": {broken{otherKey: true}, "another request's correlation key"},
		"an ended context reported as a definitive rejection":  {broken{createErr: func(error) error { return &ports.GatewayError{Kind: ports.GatewayRejected, Class: "canceled"} }}, "only UNKNOWN is safe"},
		"an ended context that still creates the order":        {broken{ignoreCtx: true}, "instead of failing"},
	}
	for name, c := range cases {
		name, c := name, c
		t.Run(name, func(t *testing.T) {
			f := gatewaytest.New()
			f.Lookup = false
			c.adapter.ServiceOrderGateway = f
			problems := gatewaytest.Check(c.adapter, "")
			if len(problems) == 0 {
				t.Fatalf("the contract accepted an adapter with %s", name)
			}
			joined := strings.Join(problems, " | ")
			if !strings.Contains(joined, c.want) {
				t.Fatalf("the contract failed it, but not for the right reason: %s", joined)
			}
		})
	}
}
