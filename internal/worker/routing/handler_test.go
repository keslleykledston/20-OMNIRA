package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeRunner struct {
	conversationID uuid.UUID
	called         bool
}

func (r *fakeRunner) RunForConversation(ctx context.Context, id uuid.UUID, fn func(context.Context) error) error {
	r.conversationID = id
	return fn(ctx)
}

type fakeAssigner struct{ calls int }

func (a *fakeAssigner) AssignRoundRobin(context.Context, uuid.UUID) (uuid.UUID, error) {
	a.calls++
	return uuid.New(), nil
}

func TestHandlerIgnoresEnvelopeTenant(t *testing.T) {
	id := uuid.New()
	runner, assigner := &fakeRunner{}, &fakeAssigner{}
	handler, err := NewHandler(runner, assigner)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"tenant_id":"` + uuid.NewString() + `","aggregate_id":"` + id.String() + `"}`)
	if err := handler.Handle(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if runner.conversationID != id || assigner.calls != 1 {
		t.Fatalf("conversation=%s calls=%d", runner.conversationID, assigner.calls)
	}
}

func TestHandlerRejectsMalformedReferencePermanently(t *testing.T) {
	handler, _ := NewHandler(&fakeRunner{}, &fakeAssigner{})
	if err := handler.Handle(context.Background(), []byte(`{"aggregate_id":"bad"}`)); !errors.Is(err, ErrPermanent) {
		t.Fatalf("error=%v", err)
	}
}
