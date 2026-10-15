package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

func TestFormatProtocol(t *testing.T) {
	if got := domain.FormatProtocol("AT", 10482); got != "AT-10482" {
		t.Fatal(got)
	}
}

func TestOpenInputNormalize(t *testing.T) {
	conv := uuid.New()
	contact := uuid.New()
	in, err := domain.OpenInput{ConversationID: &conv, ContactID: &contact}.Normalize()
	if err != nil || in.ContactID != nil || in.Source != domain.SourceOperator || in.OriginChannel != "other" {
		t.Fatalf("the contact is taken from the conversation and never trusted from the caller: %+v %v", in, err)
	}
	for name, bad := range map[string]domain.OpenInput{
		"source":  {Source: "hacker"},
		"channel": {OriginChannel: "Whats App!"},
		"key":     {IdempotencyKey: "short"},
		"nilconv": {ConversationID: &uuid.Nil},
		"ext":     {ExternalProtocol: strings.Repeat("x", 81)},
	} {
		if _, err := bad.Normalize(); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s should be invalid: %v", name, err)
		}
	}
}

func TestOpenInputHashDiffersByRequest(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	if (domain.OpenInput{ConversationID: &a}).Hash() == (domain.OpenInput{ConversationID: &b}).Hash() {
		t.Fatal("two requests must not share a fingerprint")
	}
	if (domain.OpenInput{ConversationID: &a}).Hash() != (domain.OpenInput{ConversationID: &a}).Hash() {
		t.Fatal("the same request has a stable fingerprint")
	}
}

func TestCloseInputNormalize(t *testing.T) {
	if _, err := (domain.CloseInput{}).Normalize(); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("a result is required")
	}
	in, err := domain.CloseInput{Abandon: true}.Normalize()
	if err != nil || in.ResolutionCode != domain.ResAbandoned {
		t.Fatalf("abandon defaults to the canonical result: %+v %v", in, err)
	}
	if _, err := (domain.CloseInput{ResolutionCode: "ok", Note: "Bearer abcdefghijklmnop1234"}).Normalize(); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("a credential in a note is refused")
	}
	if _, err := (domain.CloseInput{ResolutionCode: "ok", Note: strings.Repeat("a", domain.MaxNoteLen+1)}).Normalize(); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("a long note is refused")
	}
}

func TestResolutionFromFinalizeAndRefinement(t *testing.T) {
	cases := map[string]struct {
		status domain.SessionStatus
		code   string
	}{
		"resolved": {domain.SessionClosed, domain.ResInformationProvided}, "transferred": {domain.SessionClosed, domain.ResTransferred},
		"no_response": {domain.SessionAbandoned, domain.ResAbandoned}, "spam": {domain.SessionClosed, domain.ResOther}, "other": {domain.SessionClosed, domain.ResOther},
	}
	for reason, want := range cases {
		st, code := domain.ResolutionFromFinalize(reason)
		if st != want.status || code != want.code {
			t.Errorf("%s -> %s %s", reason, st, code)
		}
	}
	if domain.RefineResolution(domain.ResInformationProvided, 1, 2, 3) != domain.ResExternalServiceOrder ||
		domain.RefineResolution(domain.ResInformationProvided, 0, 1, 3) != domain.ResCaseCreated ||
		domain.RefineResolution(domain.ResInformationProvided, 0, 0, 3) != domain.ResTaskCreated ||
		domain.RefineResolution(domain.ResInformationProvided, 0, 0, 0) != domain.ResInformationProvided {
		t.Fatal("the strongest link wins")
	}
	if domain.RefineResolution(domain.ResTransferred, 1, 1, 1) != domain.ResTransferred {
		t.Fatal("only a plain 'information provided' is refined")
	}
}

func TestEventValidationKeepsTextOutOfPayloads(t *testing.T) {
	ev := domain.ContextEvent{TenantID: uuid.New(), EventType: domain.EvSessionOpened, EntityType: "service_session", EntityID: uuid.New(),
		Payload: map[string]any{"channel": "whatsapp", "n": 1, "ok": true}}
	if err := ev.Validate(); err != nil {
		t.Fatal(err)
	}
	ev.Payload = map[string]any{"text": strings.Repeat("x", 65)}
	if err := ev.Validate(); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("a long text in a payload is refused")
	}
	ev.Payload = map[string]any{"nested": map[string]any{"a": 1}}
	if err := ev.Validate(); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("a nested structure in a payload is refused")
	}
}
