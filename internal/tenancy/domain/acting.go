package domain

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// ActingAsHeader — how the client DECLARES the context a request acts in (ADR-0040 section 4). It only selects between contexts the
// server then validates on its own: it grants nothing, and a tenant in the URL never does either.
const ActingAsHeader = "X-Omnira-Acting-As"

// ActingAs — the context one request acts in. A request acts as a member of the instance OR as a Hub agent for one hub, never as the
// sum of both: a person who is both gets exactly the privileges of the context they asked for.
type ActingAs struct {
	HubID uuid.UUID // uuid.Nil = acting as a member
}

// IsHub — the request asks to act as a Hub agent.
func (a ActingAs) IsHub() bool { return a.HubID != uuid.Nil }

// String — the form that goes into the audit trail ("member" or "hub:<id>").
func (a ActingAs) String() string {
	if a.IsHub() {
		return "hub:" + a.HubID.String()
	}
	return "member"
}

var ErrInvalidActingAs = errors.New("invalid acting context")

// ParseActingAs — "" and "member" mean the member context (what every request did before this header existed); "hub:<uuid>" the
// delegated one. Anything else is an error, never a silent fallback: a typo must not turn a delegated request into a member one.
func ParseActingAs(raw string) (ActingAs, error) {
	v := strings.TrimSpace(raw)
	if v == "" || v == "member" {
		return ActingAs{}, nil
	}
	rest, ok := strings.CutPrefix(v, "hub:")
	if !ok {
		return ActingAs{}, ErrInvalidActingAs
	}
	id, err := uuid.Parse(rest)
	if err != nil || id == uuid.Nil {
		return ActingAs{}, ErrInvalidActingAs
	}
	return ActingAs{HubID: id}, nil
}
