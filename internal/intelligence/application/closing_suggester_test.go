package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	aiports "github.com/omnira/omnira/internal/ai/ports"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// recordingReader tells which way the text was read: the plain read, or the one a Hub agent's draft needs (ADR-0040 phase 07), which carries the holder.
type recordingReader struct {
	plain, held int
	holder      uuid.UUID
	heldEmpty   bool // the statement found the conversation no longer held
}

func (r *recordingReader) RecentMessages(context.Context, uuid.UUID, uuid.UUID, int) ([]ports.ClosingMessage, error) {
	r.plain++
	return []ports.ClosingMessage{{Role: "customer", Text: "oi", At: time.Now()}}, nil
}

func (r *recordingReader) RecentMessagesHeldBy(_ context.Context, _, _, holder uuid.UUID, _ int) ([]ports.ClosingMessage, error) {
	r.held++
	r.holder = holder
	if r.heldEmpty {
		return nil, nil
	}
	return []ports.ClosingMessage{{Role: "customer", Text: "oi", At: time.Now()}}, nil
}

type countingGenerator struct{ calls int }

func (g *countingGenerator) Generate(context.Context, aiports.GenerateRequest) (aiports.GenerateResponse, error) {
	g.calls++
	return aiports.GenerateResponse{OutputText: `{"summary":"ok","follow_ups":[]}`}, nil
}

func newTestSuggester(r ports.ClosingReader, g aiports.TextGenerator) *ClosingSuggester {
	router := NewModelRouter().Set(TaskClosingSuggest, ModelRoute{Provider: "fake", Model: "fake", Generator: g, MaxOutputTokens: 100})
	s := NewClosingSuggester(r, router, Flags{CopilotEnabled: true})
	s.MinEvery = 0
	return s
}

func testContext() context.Context {
	tc, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	return tenancydomain.WithTenantContext(context.Background(), tc)
}

func TestSuggestReadsTheTextThePlainWay(t *testing.T) {
	r, g := &recordingReader{}, &countingGenerator{}
	if _, err := newTestSuggester(r, g).Suggest(testContext(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if r.plain != 1 || r.held != 0 || g.calls != 1 {
		t.Errorf("plain=%d held=%d provider=%d, want the plain read once", r.plain, r.held, g.calls)
	}
}

func TestSuggestHeldByReadsOnlyWhileThePersonHoldsTheConversation(t *testing.T) {
	holder := uuid.New()
	r, g := &recordingReader{}, &countingGenerator{}
	if _, err := newTestSuggester(r, g).SuggestHeldBy(testContext(), uuid.New(), holder); err != nil {
		t.Fatal(err)
	}
	if r.held != 1 || r.plain != 0 || r.holder != holder {
		t.Errorf("held=%d plain=%d holder=%v, want one holder-bound read for the holder", r.held, r.plain, r.holder)
	}

	// the conversation changed hands before the read: nothing is read, and nothing reaches the provider
	r, g = &recordingReader{heldEmpty: true}, &countingGenerator{}
	if _, err := newTestSuggester(r, g).SuggestHeldBy(testContext(), uuid.New(), holder); !errors.Is(err, ErrClosingNothing) {
		t.Errorf("err = %v, want ErrClosingNothing", err)
	}
	if g.calls != 0 {
		t.Error("the provider was reached for a conversation the person no longer held")
	}
}
