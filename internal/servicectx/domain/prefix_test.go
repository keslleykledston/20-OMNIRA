package domain_test

import (
	"testing"

	"github.com/omnira/omnira/internal/servicectx/domain"
)

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
