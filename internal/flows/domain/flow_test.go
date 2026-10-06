package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestNewFlowValidates(t *testing.T) {
	tenant := uuid.New()
	if _, err := NewFlow(uuid.Nil, "ok", "Ok", FlowTypeInbound, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil tenant must be invalid: %v", err)
	}
	for _, slug := range []string{"", "UPPER", "has space", "-lead", "trail-", "a_b", string(make([]byte, 70))} {
		if _, err := NewFlow(tenant, slug, "Name", FlowTypeInbound, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("slug %q must be rejected: %v", slug, err)
		}
	}
	if _, err := NewFlow(tenant, "ok", "   ", FlowTypeInbound, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("blank name must be rejected")
	}
	if _, err := NewFlow(tenant, "ok", "Name", FlowType("NOPE"), nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown type must be rejected")
	}
	f, err := NewFlow(tenant, "smart-reception", "  Smart Reception ", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != FlowTypeInbound || f.Status != FlowStatusDraft || f.DraftRevision != 1 || f.Name != "Smart Reception" || f.RestartPolicy != RestartNewConversationOnly {
		t.Fatalf("unexpected defaults: %+v", f)
	}
	if f.ActiveVersionID != nil {
		t.Fatal("a draft has no active version")
	}
}

func TestTriggerFilterMatches(t *testing.T) {
	line1, line2 := uuid.New(), uuid.New()
	if !(TriggerFilter{}).Matches(line1, "waha") {
		t.Fatal("an empty filter applies to every line")
	}
	f := TriggerFilter{ConnectionIDs: []uuid.UUID{line1}, Providers: []string{"meta_cloud"}}
	if !f.Matches(line1, "META_CLOUD") {
		t.Fatal("provider match must be case-insensitive")
	}
	if f.Matches(line2, "meta_cloud") || f.Matches(line1, "waha") {
		t.Fatal("both connection and provider must match when both are set")
	}
}

func TestRunStatusActive(t *testing.T) {
	for s, want := range map[RunStatus]bool{RunRunning: true, RunWaitingInput: true, RunWaitingHuman: true, RunCompleted: false, RunFailed: false, RunCancelled: false, RunExpired: false} {
		if s.Active() != want {
			t.Errorf("%s: Active()=%v want %v", s, s.Active(), want)
		}
	}
}
