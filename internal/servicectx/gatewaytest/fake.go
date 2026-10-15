// Package gatewaytest is a scriptable stand-in for an ERP, to test the module's behavior around the dangerous cases (a timeout after the ERP processed the request, a
// rejection, a crash inside an adapter). It is NEVER installed in a running system.
package gatewaytest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// Mode says what the next CreateServiceOrder does.
type Mode int

const (
	// Succeed creates the order and answers.
	Succeed Mode = iota
	// LoseAnswer creates the order in the "ERP" and then fails with an UNKNOWN error: the nightmare (it was processed, the caller does not know).
	LoseAnswer
	// DropRequest fails with an UNKNOWN error WITHOUT creating anything (the request never arrived).
	DropRequest
	// Reject refuses definitively.
	Reject
	// Panic crashes inside the adapter.
	Panic
	// Hang blocks until Release is called (or the call's context ends), then behaves as Succeed.
	Hang
)

// Fake is an in-memory ERP.
type Fake struct {
	mu          sync.Mutex
	records     map[string]domain.Snapshot
	byCorr      map[string]string
	next        int
	CreateCalls int
	GetCalls    int
	Mode        Mode
	// Lookup says whether the ERP can find a record again by correlation key (the optional capability).
	Lookup bool
	// Down makes every call fail with an UNKNOWN error (the ERP is unreachable).
	Down    bool
	Called  chan struct{} // signalled when a create call reaches the ERP (Hang)
	release chan struct{}
	Clock   func() time.Time
}

func New() *Fake {
	return &Fake{records: map[string]domain.Snapshot{}, byCorr: map[string]string{}, Called: make(chan struct{}, 8), release: make(chan struct{}), Clock: time.Now, Lookup: true}
}

func key(t, id string) string { return t + "|" + id }

func (f *Fake) System() string { return "fake_erp" }

// Put places a record in the ERP.
func (f *Fake) Put(s domain.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.UpdatedAt == nil {
		t := f.Clock().UTC()
		s.UpdatedAt = &t
	}
	f.records[key(s.ExternalType, s.ExternalID)] = s
}

func (f *Fake) Remove(t, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, key(t, id))
}

func (f *Fake) Count(t string) (n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.records {
		if r.ExternalType == t {
			n++
		}
	}
	return n
}

func (f *Fake) Release() { close(f.release) }

func (f *Fake) Get(ctx context.Context, t, id string) (*domain.Snapshot, error) {
	f.mu.Lock()
	f.GetCalls++
	down := f.Down
	r, ok := f.records[key(t, id)]
	f.mu.Unlock()
	if down {
		return nil, &ports.GatewayError{Kind: ports.GatewayUnknown, Class: "timeout"}
	}
	if !ok {
		return nil, &ports.GatewayError{Kind: ports.GatewayNotFound, Class: "not_found"}
	}
	return &r, nil
}

func (f *Fake) CreateServiceOrder(ctx context.Context, req ports.CreateOrderRequest) (*domain.Snapshot, error) {
	f.mu.Lock()
	f.CreateCalls++
	mode := f.Mode
	down := f.Down
	f.mu.Unlock()
	select {
	case f.Called <- struct{}{}:
	default:
	}
	if down {
		return nil, &ports.GatewayError{Kind: ports.GatewayUnknown, Class: "timeout"}
	}
	switch mode {
	case Reject:
		return nil, &ports.GatewayError{Kind: ports.GatewayRejected, Class: "invalid_customer"}
	case Panic:
		panic("adapter crashed")
	case DropRequest:
		return nil, &ports.GatewayError{Kind: ports.GatewayUnknown, Class: "timeout"}
	case Hang:
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, &ports.GatewayError{Kind: ports.GatewayUnknown, Class: "timeout", Err: ctx.Err()}
		}
	}
	f.mu.Lock()
	f.next++
	id := fmt.Sprintf("%d", 1000+f.next)
	t := f.Clock().UTC()
	snap := domain.Snapshot{ExternalType: domain.ExternalTypeServiceOrder, ExternalID: id, DisplayRef: "OS " + id, ExternalStatus: "Aberta", StatusClass: domain.StatusOpen, UpdatedAt: &t}
	f.records[key(snap.ExternalType, id)] = snap
	f.byCorr[req.CorrelationKey] = key(snap.ExternalType, id)
	f.mu.Unlock()
	if mode == LoseAnswer {
		return nil, &ports.GatewayError{Kind: ports.GatewayUnknown, Class: "timeout"}
	}
	return &snap, nil
}

// lookupFake exposes the optional capability only when Lookup is on.
type lookupFake struct{ *Fake }

func (l lookupFake) FindByCorrelation(ctx context.Context, corr string) (*domain.Snapshot, error) {
	l.mu.Lock()
	k, ok := l.byCorr[corr]
	down := l.Down
	r := l.records[k]
	l.mu.Unlock()
	if down {
		return nil, &ports.GatewayError{Kind: ports.GatewayUnknown, Class: "timeout"}
	}
	if !ok {
		return nil, &ports.GatewayError{Kind: ports.GatewayNotFound, Class: "not_found"}
	}
	return &r, nil
}

// Resolver hands the fake to every integration (the tests seed the integrations of the company).
type Resolver struct{ F *Fake }

func (r Resolver) Resolve(ctx context.Context, _ uuid.UUID) (ports.ServiceOrderGateway, error) {
	if r.F.Lookup {
		return lookupFake{r.F}, nil
	}
	return r.F, nil
}
