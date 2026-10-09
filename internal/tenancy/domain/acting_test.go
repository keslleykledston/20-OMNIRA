package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestParseActingAs(t *testing.T) {
	hub := uuid.New()
	for _, c := range []struct {
		in      string
		hub     uuid.UUID
		wantErr bool
	}{
		{"", uuid.Nil, false},
		{"   ", uuid.Nil, false},
		{"member", uuid.Nil, false},
		{" member ", uuid.Nil, false},
		{"hub:" + hub.String(), hub, false},
		{"hub:", uuid.Nil, true},
		{"hub:not-a-uuid", uuid.Nil, true},
		{"hub:" + uuid.Nil.String(), uuid.Nil, true},
		{"Hub:" + hub.String(), uuid.Nil, true},
		{"HUB:" + hub.String(), uuid.Nil, true},
		{"tenant:" + hub.String(), uuid.Nil, true},
		{"member,hub:" + hub.String(), uuid.Nil, true},
		{"hub:" + hub.String() + ",member", uuid.Nil, true},
		{"admin", uuid.Nil, true},
	} {
		got, err := ParseActingAs(c.in)
		if c.wantErr {
			if !errors.Is(err, ErrInvalidActingAs) {
				t.Errorf("%q: want ErrInvalidActingAs, got %v", c.in, err)
			}
			continue
		}
		if err != nil || got.HubID != c.hub {
			t.Errorf("%q: got %+v, %v", c.in, got, err)
		}
	}
}

func TestActingAsString(t *testing.T) {
	hub := uuid.New()
	if s := (ActingAs{}).String(); s != "member" {
		t.Errorf("member = %q", s)
	}
	if s := (ActingAs{HubID: hub}).String(); s != "hub:"+hub.String() {
		t.Errorf("hub = %q", s)
	}
	if !(ActingAs{HubID: hub}).IsHub() || (ActingAs{}).IsHub() {
		t.Error("IsHub")
	}
}

func TestHubServeContext(t *testing.T) {
	tn, u, h, c, g := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	perms := []string{"conversation.read"}
	tc, err := NewHubServeTenantContext(tn, u, h, c, g, perms, "r")
	if err != nil {
		t.Fatal(err)
	}
	perms[0] = "membership.manage" // the caller's slice must not reach into the context
	if tc.Source != AccessSourceHubServe || tc.Permissions[0] != "conversation.read" || tc.ActingAs() != "hub:"+h.String() {
		t.Errorf("context = %+v", tc)
	}
	// every id is mandatory: a delegated context with a missing link must not exist
	for i, args := range [][5]uuid.UUID{{uuid.Nil, u, h, c, g}, {tn, uuid.Nil, h, c, g}, {tn, u, uuid.Nil, c, g}, {tn, u, h, uuid.Nil, g}, {tn, u, h, c, uuid.Nil}} {
		if _, err := NewHubServeTenantContext(args[0], args[1], args[2], args[3], args[4], nil, ""); err == nil {
			t.Errorf("case %d: a missing id must be refused", i)
		}
	}
	// never a member context, never a management context
	if tc.MayManageAsTenant() {
		t.Error("delegated serving must not reach the channel-management services")
	}
	if tc.ActingAs() == "member" {
		t.Error("a delegated context must not call itself a member")
	}
	if _, err := NewTenantContext(tn, u, AccessSourceHubServe); err == nil {
		t.Error("NewTenantContext must not mint a delegated context")
	}
	direct, _ := NewTenantContext(tn, u, AccessSourceDirect)
	if direct.ActingAs() != "member" {
		t.Errorf("direct = %q", direct.ActingAs())
	}
}
