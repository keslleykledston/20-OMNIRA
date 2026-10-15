package domain_test

import (
	"testing"

	"github.com/omnira/omnira/internal/servicectx/domain"
)

func TestPrefixCandidatesComeFromTheLettersOfTheName(t *testing.T) {
	for name, first := range map[string]string{
		"Veloso Telecom":   "VEL",
		"INFORR":           "INF",
		"K3G Solutions":    "KGS", // digits are not letters: the administrator chooses KTG
		"Conexão São José": "CON",
		"  ágata   ":       "AGA",
		"Zé":               "ZEX", // two letters are padded to three
		"12345":            "ATX", // no letters at all: the default prefix, padded
		"Ünïcode Ltda":     "UNI",
	} {
		got := domain.PrefixCandidates(name)
		if len(got) == 0 || got[0] != first {
			t.Errorf("%q -> %v, want first %s", name, got[:min(3, len(got))], first)
		}
	}
}

func TestPrefixCandidatesAreValidDistinctAndLongerWhenTheShortOnesAreTaken(t *testing.T) {
	got := domain.PrefixCandidates("Veloso Telecom")
	if got[0] != "VEL" || got[1] != "VELO" || got[2] != "VELOS" || got[3] != "VELOSO" || got[4] != "VEA" {
		t.Fatalf("order: %v", got[:6])
	}
	seen := map[string]bool{}
	for _, c := range got {
		if !domain.ValidPrefix(c) || seen[c] {
			t.Fatalf("invalid or repeated candidate %q", c)
		}
		seen[c] = true
	}
	if len(got) < 100 {
		t.Fatalf("a company whose first choices are taken still has plenty to try: %d", len(got))
	}
}

func TestValidPrefixIsCapitalLettersOnly(t *testing.T) {
	for p, ok := range map[string]bool{"KTG": true, "VLS": true, "INF": true, "AB": true, "ABCDEF": true,
		"K3G": false, "ktg": false, "A": false, "ABCDEFG": false, "": false, "AB-": false, "AÇB": false, " KTG": false} {
		if domain.ValidPrefix(p) != ok {
			t.Errorf("ValidPrefix(%q) = %v", p, !ok)
		}
	}
	if domain.NormalizePrefix("  ktg ") != "KTG" {
		t.Error("a typed prefix is trimmed and upper-cased")
	}
}

func TestTheDefaultsAreTheOwnersDecisions(t *testing.T) {
	if domain.DefaultSessionStart != 1001 {
		t.Errorf("protocols start at 1001: %d", domain.DefaultSessionStart)
	}
	if domain.DefaultReopenWindow.Hours() != 24 {
		t.Errorf("a closed attendance can be reopened for 24 hours: %v", domain.DefaultReopenWindow)
	}
}
