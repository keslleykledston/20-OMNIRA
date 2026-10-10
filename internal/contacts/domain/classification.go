package domain

import (
	"errors"

	"github.com/google/uuid"
)

// ContactKind is THE classification of an external contact (ADR-0014 + ADR-0018). Staff are Users, never contacts.
type ContactKind string

const (
	KindUnclassified ContactKind = "unclassified"
	KindCustomer     ContactKind = "customer"
	KindOther        ContactKind = "other"
	// KindInternal is an external person an operator declared internal: the team on a personal number, a partner or a
	// supplier (ADR-0018 addendum). It is NOT the verified staff identity (a User): it only switches customer automation
	// off, it never grants anything. It always carries an InternalRole.
	KindInternal ContactKind = "internal"
	// KindSpam is a safety state, orthogonal to the customer/other question.
	KindSpam ContactKind = "spam"
)

func (k ContactKind) Valid() bool {
	return k == KindUnclassified || k == KindCustomer || k == KindOther || k == KindInternal || k == KindSpam
}

// InternalRole says which kind of internal contact it is. It exists exactly while the kind is internal.
type InternalRole string

const (
	RoleTeam     InternalRole = "team"
	RolePartner  InternalRole = "partner"
	RoleSupplier InternalRole = "supplier"
)

func (r InternalRole) Valid() bool { return r == RoleTeam || r == RolePartner || r == RoleSupplier }

// ClassificationSource says who decided. An AI suggestion only counts as a source after a human confirmed it.
type ClassificationSource string

const (
	SourceManual                ClassificationSource = "manual"
	SourceImport                ClassificationSource = "import"
	SourceTrustedCRM            ClassificationSource = "trusted_crm"
	SourceTicketFlow            ClassificationSource = "ticket_flow"
	SourceRule                  ClassificationSource = "rule"
	SourceAISuggestionConfirmed ClassificationSource = "ai_suggestion_confirmed"
	SourceMigration             ClassificationSource = "migration"
	SourceBackfill              ClassificationSource = "backfill"
)

func (s ClassificationSource) Valid() bool {
	switch s {
	case SourceManual, SourceImport, SourceTrustedCRM, SourceTicketFlow, SourceRule, SourceAISuggestionConfirmed, SourceMigration, SourceBackfill:
		return true
	}
	return false
}

type RelationshipType string

const (
	RelEmployee       RelationshipType = "employee"
	RelOwner          RelationshipType = "owner"
	RelTechnical      RelationshipType = "technical_contact"
	RelBilling        RelationshipType = "billing_contact"
	RelAdministrative RelationshipType = "administrative_contact"
	RelRepresentative RelationshipType = "representative"
	RelContractor     RelationshipType = "contractor"
	RelOther          RelationshipType = "other"
)

func (r RelationshipType) Valid() bool {
	switch r {
	case RelEmployee, RelOwner, RelTechnical, RelBilling, RelAdministrative, RelRepresentative, RelContractor, RelOther:
		return true
	}
	return false
}

var (
	ErrContactNotFound = errors.New("contacts: contact not found")
	ErrAccountMissing  = errors.New("contacts: account not found or archived")
	// ErrInternalNeedsRole: an internal contact must say whether it is team, partner or supplier.
	ErrInternalNeedsRole = errors.New("contacts: an internal contact needs a role (team, partner or supplier)")
	ErrInvalidInput      = errors.New("contacts: invalid classification input")
	// ErrInternalIsTheInstancesCall: a contact the instance declared internal is not reclassified by a Hub agent (ADR-0040 phase 04a).
	ErrInternalIsTheInstancesCall = errors.New("contacts: an internal contact is the instance's own call")

	// ErrCustomerNeedsAccount: customer needs >= 1 active account link (atomic with the transition).
	ErrCustomerNeedsAccount = errors.New("contacts: a customer needs at least one active account link")
	// ErrLastLink: removing the last active link of a customer without reclassifying in the same transaction.
	ErrLastLink     = errors.New("contacts: cannot end the last active link of a customer without reclassifying")
	ErrLinkNotFound = errors.New("contacts: link not found")
)

// AccountLinkInput describes one contact<->account link to create or refresh.
type AccountLinkInput struct {
	AccountID       uuid.UUID
	Relationship    RelationshipType
	Primary         bool
	Confidence      *float64
	VerifiedByActor bool
	// Source overrides the call's source for THIS link only (e.g. ticket_flow when a human accepted a suggestion that
	// came from a validated ticket selection). Empty = the call's source.
	Source ClassificationSource
}
