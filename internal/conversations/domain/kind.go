package domain

// Kind is what a conversation (or a group) is, derived deterministically from who takes part (ADR-0018). There is no
// "mixed" kind: a customer conversation that also has an unclassified person is customer_service with
// HasUnclassifiedParticipants.
type Kind string

const (
	KindInternal        Kind = "internal"
	KindCustomerService Kind = "customer_service"
	KindExternalOther   Kind = "external_other"
	KindUnclassified    Kind = "unclassified"
)

func (k Kind) Valid() bool {
	return k == KindInternal || k == KindCustomerService || k == KindExternalOther || k == KindUnclassified
}

// ParticipantClass is how ONE participant counts. Only a VERIFIED internal identity makes a participant internal.
type ParticipantClass string

const (
	ClassInternal     ParticipantClass = "internal"
	ClassCustomer     ParticipantClass = "customer"
	ClassOther        ParticipantClass = "other"
	ClassUnclassified ParticipantClass = "unclassified"
)

// ClassifyKind applies the rules, in order:
//  1. no known participant                       -> unclassified
//  2. any customer                               -> customer_service (agents present do NOT make it internal)
//  3. any unclassified external                  -> unclassified
//  4. any "other" external                       -> external_other
//  5. only verified internal users               -> internal
//
// hasUnclassified is true whenever at least one participant is unclassified, whatever the kind.
func ClassifyKind(parts []ParticipantClass) (kind Kind, hasUnclassified bool) {
	var internal, customer, other, unclassified int
	for _, p := range parts {
		switch p {
		case ClassInternal:
			internal++
		case ClassCustomer:
			customer++
		case ClassOther:
			other++
		default: // anything unknown is NOT trusted as anything: it counts as unclassified
			unclassified++
		}
	}
	hasUnclassified = unclassified > 0
	switch {
	case internal+customer+other+unclassified == 0:
		return KindUnclassified, false
	case customer > 0:
		return KindCustomerService, hasUnclassified
	case unclassified > 0:
		return KindUnclassified, true
	case other > 0:
		return KindExternalOther, false
	default:
		return KindInternal, false
	}
}

// KindFromContactKind is the kind of a 1:1 conversation with ONE external contact; it must agree with the SQL function
// contact_kind_to_conversation_kind (tested against the database). Spam is "other": no customer automation either way.
func KindFromContactKind(contactKind string) Kind {
	switch contactKind {
	case "customer":
		return KindCustomerService
	case "unclassified":
		return KindUnclassified
	default:
		return KindExternalOther
	}
}

// AllowsCustomerAutomation says whether customer-facing automation (bot, automatic ticket, SLA, CSAT, waiting metrics)
// may act: only on a customer service conversation. internal, external_other and unclassified never trigger it.
func (k Kind) AllowsCustomerAutomation() bool { return k == KindCustomerService }
