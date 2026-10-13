package eligibility

import (
	"reflect"
	"testing"
)

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }

func base() Facts {
	return Facts{Member: true, Active: true, Available: true, QueueOpen: true, Workload: 0, Capacity: 2}
}

func TestEligibleWhenEveryRuleHolds(t *testing.T) {
	f := base()
	f.ShiftOpen = yes()
	f.PresenceReq, f.PresenceOK = true, true
	if v := Evaluate(f, Policy{}); !v.Eligible || len(v.Reasons) != 0 {
		t.Fatalf("%+v", v)
	}
	if v := Evaluate(base(), Policy{}); !v.Eligible { // no shift calendar, presence not required: nothing else restricts
		t.Fatalf("no restriction must be eligible: %+v", v)
	}
}

// Quatro conceitos independentes: cada um sozinho basta para recusar, e todos os motivos são listados.
func TestEachConceptRefusesOnItsOwnAndAllReasonsAreListed(t *testing.T) {
	cases := map[string]struct {
		mut  func(*Facts)
		want []Reason
	}{
		"sector closed (24x7 means open, not that agents work)": {func(f *Facts) { f.QueueOpen = false }, []Reason{QueueClosed}},
		"off shift":                      {func(f *Facts) { f.ShiftOpen = no() }, []Reason{OffShift}},
		"offline":                        {func(f *Facts) { f.PresenceReq, f.PresenceOK = true, false }, []Reason{Offline}},
		"presence down":                  {func(f *Facts) { f.PresenceReq, f.PresenceDown = true, true }, []Reason{PresenceUnavailable}},
		"capacity full":                  {func(f *Facts) { f.Workload = 2 }, []Reason{NoCapacity}},
		"over capacity":                  {func(f *Facts) { f.Workload = 5 }, []Reason{NoCapacity}},
		"unavailable":                    {func(f *Facts) { f.Available = false }, []Reason{Unavailable}},
		"inactive":                       {func(f *Facts) { f.Active = false }, []Reason{Inactive}},
		"not a member":                   {func(f *Facts) { f.Member = false }, []Reason{NotMember}},
		"several at once":                {func(f *Facts) { f.QueueOpen, f.Workload, f.ShiftOpen = false, 9, no() }, []Reason{QueueClosed, OffShift, NoCapacity}},
		"presence unknown beats offline": {func(f *Facts) { f.PresenceReq, f.PresenceDown, f.PresenceOK = true, true, false }, []Reason{PresenceUnavailable}},
	}
	for name, c := range cases {
		f := base()
		c.mut(&f)
		v := Evaluate(f, Policy{})
		if v.Eligible || !reflect.DeepEqual(v.Reasons, c.want) {
			t.Errorf("%s: %+v, want %v", name, v, c.want)
		}
	}
}

// Plantão: o período de plantão é a escala; o agente não precisa ser membro da fila, mas continua sujeito a ativo, presença e capacidade.
func TestOnCallReplacesMembershipAndShiftButNotActivityPresenceOrCapacity(t *testing.T) {
	f := Facts{OnCall: true, Active: true, QueueOpen: true, Capacity: 3, ShiftOpen: no()}
	if v := Evaluate(f, Policy{}); !v.Eligible || !v.OnCall {
		t.Fatalf("on call, not a member, own shift closed: %+v", v)
	}
	f.Active = false
	if v := Evaluate(f, Policy{}); v.Eligible || !reflect.DeepEqual(v.Reasons, []Reason{Inactive}) {
		t.Fatalf("inactive on-call agent: %+v", v)
	}
	f.Active, f.Workload = true, 3
	if v := Evaluate(f, Policy{}); v.Eligible || !reflect.DeepEqual(v.Reasons, []Reason{NoCapacity}) {
		t.Fatalf("on-call agent at capacity: %+v", v)
	}
	f.Workload, f.PresenceReq = 0, true
	if v := Evaluate(f, Policy{}); v.Eligible || !reflect.DeepEqual(v.Reasons, []Reason{Offline}) {
		t.Fatalf("on-call agent offline: %+v", v)
	}
}

// Política explícita: só relaxa o que ela nomeia; capacidade, presença e atividade nunca.
func TestExplicitPolicyRelaxesOnlyWhatItNames(t *testing.T) {
	f := base()
	f.ShiftOpen, f.QueueOpen = no(), false
	if v := Evaluate(f, Policy{AllowOffShift: true}); v.Eligible || !reflect.DeepEqual(v.Reasons, []Reason{QueueClosed}) {
		t.Fatalf("AllowOffShift must not open a closed sector: %+v", v)
	}
	if v := Evaluate(f, Policy{AllowOffShift: true, AllowClosed: true}); !v.Eligible {
		t.Fatalf("both allowed: %+v", v)
	}
	f.Workload = 2
	if v := Evaluate(f, Policy{AllowOffShift: true, AllowClosed: true}); v.Eligible {
		t.Fatal("no policy relaxes capacity")
	}
	f = base()
	f.Active = false
	if v := Evaluate(f, Policy{AllowOffShift: true, AllowClosed: true}); v.Eligible {
		t.Fatal("no policy relaxes activity")
	}
}
