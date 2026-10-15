// Package eligibility is the ONE place that says whether an agent may receive an automatic assignment in a queue at an instant, and, when
// not, every reason why (ADR-0044 §3). The conjunction it evaluates keeps four independent concepts independent: the sector's calendar,
// the agent's own shift, presence and capacity. Pure domain: the facts come from the adapters.
package eligibility

import "sort"

// Reason is a closed set; the monitoring screen and the audit trail show exactly these values.
type Reason string

const (
	QueueClosed         Reason = "queue_closed"         // the calendar of the queue/sector is closed now
	NotMember           Reason = "not_member"           // neither a member of the queue nor on call for it
	Inactive            Reason = "inactive"             // membership or agent profile not active
	Unavailable         Reason = "unavailable"          // the member marked themselves unavailable in this queue
	OffShift            Reason = "off_shift"            // the agent's own shift calendar is closed now
	Offline             Reason = "offline"              // presence is required and the agent is not online
	PresenceUnavailable Reason = "presence_unavailable" // presence is required but cannot be read: fail closed, never assume online
	NoCapacity          Reason = "no_capacity"          // open conversations >= capacity (capacity 0 = no limit)
)

// Facts are what the adapters learned about one agent for one queue at one instant. A nil pointer means "no such restriction".
type Facts struct {
	Member       bool
	OnCall       bool // inside an active on-call/contingency period that covers this queue
	Active       bool
	Available    bool
	ShiftOpen    *bool // nil: the agent has no shift calendar for this queue (no restriction)
	QueueOpen    bool
	PresenceReq  bool
	PresenceOK   bool // meaningful only when PresenceReq
	PresenceDown bool // presence is required but the backend could not answer
	Workload     int
	Capacity     int
}

// Policy lets an explicit, authorized step relax a rule (ADR-0044: contingency with allow_off_shift). Never defaulted.
type Policy struct {
	AllowOffShift bool
	AllowClosed   bool // a contingency step may route while the sector is closed (the destination decides, not the origin)
}

// Verdict is the answer for one agent.
type Verdict struct {
	Eligible bool
	Reasons  []Reason // empty when eligible; otherwise every failing rule, in a stable order
	OnCall   bool
}

// Evaluate applies the conjunction. An agent on call is eligible although not a member (the roster is the authority), but is still
// subject to being active, the shift (an on-call period IS their shift: ShiftOpen is ignored for them), presence and capacity.
func Evaluate(f Facts, p Policy) Verdict {
	var why []Reason
	if !f.Member && !f.OnCall {
		why = append(why, NotMember)
	}
	if !f.Active {
		why = append(why, Inactive)
	}
	if f.Member && !f.OnCall && !f.Available {
		why = append(why, Unavailable)
	}
	if !f.QueueOpen && !p.AllowClosed {
		why = append(why, QueueClosed)
	}
	if !f.OnCall && f.ShiftOpen != nil && !*f.ShiftOpen && !p.AllowOffShift {
		why = append(why, OffShift)
	}
	if f.PresenceReq {
		switch {
		case f.PresenceDown:
			why = append(why, PresenceUnavailable)
		case !f.PresenceOK:
			why = append(why, Offline)
		}
	}
	if f.Capacity > 0 && f.Workload >= f.Capacity { // 0 = no limit of simultaneous conversations
		why = append(why, NoCapacity)
	}
	sort.SliceStable(why, func(i, j int) bool { return order[why[i]] < order[why[j]] })
	return Verdict{Eligible: len(why) == 0, Reasons: why, OnCall: f.OnCall}
}

var order = map[Reason]int{NotMember: 0, Inactive: 1, Unavailable: 2, QueueClosed: 3, OffShift: 4, PresenceUnavailable: 5, Offline: 6, NoCapacity: 7}
