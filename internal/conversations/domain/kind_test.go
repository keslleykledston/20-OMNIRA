package domain

import "testing"

func TestClassifyKind(t *testing.T) {
	I, C, O, U := ClassInternal, ClassCustomer, ClassOther, ClassUnclassified
	cases := []struct {
		name  string
		parts []ParticipantClass
		kind  Kind
		unc   bool
	}{
		{"empty", nil, KindUnclassified, false},
		{"one verified internal (DM with staff)", []ParticipantClass{I}, KindInternal, false},
		{"all verified internal humans", []ParticipantClass{I, I, I}, KindInternal, false},
		{"one customer", []ParticipantClass{C}, KindCustomerService, false},
		{"customer with agents present stays customer service", []ParticipantClass{C, I, I}, KindCustomerService, false},
		{"customer + unclassified is customer service WITH the flag (no mixed kind)", []ParticipantClass{C, U}, KindCustomerService, true},
		{"customer + other", []ParticipantClass{C, O}, KindCustomerService, false},
		{"only unclassified", []ParticipantClass{U}, KindUnclassified, true},
		{"internal + unclassified", []ParticipantClass{I, U}, KindUnclassified, true},
		{"only other", []ParticipantClass{O}, KindExternalOther, false},
		{"internal + other", []ParticipantClass{I, O}, KindExternalOther, false},
		{"other + unclassified", []ParticipantClass{O, U}, KindUnclassified, true},
		{"an unknown class is never trusted", []ParticipantClass{I, "weird"}, KindUnclassified, true},
		{"order does not matter", []ParticipantClass{U, I, C, O}, KindCustomerService, true},
	}
	for _, c := range cases {
		if k, u := ClassifyKind(c.parts); k != c.kind || u != c.unc {
			t.Errorf("%s: got (%s,%v), want (%s,%v)", c.name, k, u, c.kind, c.unc)
		}
	}
}

func TestOnlyCustomerServiceAllowsCustomerAutomation(t *testing.T) {
	for _, k := range []Kind{KindInternal, KindExternalOther, KindUnclassified, "", "weird"} {
		if k.AllowsCustomerAutomation() {
			t.Errorf("%q must not allow customer automation", k)
		}
	}
	if !KindCustomerService.AllowsCustomerAutomation() {
		t.Error("customer_service must allow it")
	}
}

func TestKindFromContactKind(t *testing.T) {
	for in, want := range map[string]Kind{"customer": KindCustomerService, "unclassified": KindUnclassified, "other": KindExternalOther, "spam": KindExternalOther} {
		if got := KindFromContactKind(in); got != want {
			t.Errorf("%s = %s, want %s", in, got, want)
		}
	}
}
