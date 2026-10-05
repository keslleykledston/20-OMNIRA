package domain

import "testing"

func TestNormalizePhone(t *testing.T) {
	for in, want := range map[string]string{
		"+55 (92) 99999-0001": "+5592999990001",
		"005592999990001":     "+5592999990001",
		"5592999990001":       "+5592999990001", // provider id: digits with the country code
		" +1.202.555.0100 ":   "+12025550100",
	} {
		if got, err := NormalizePhone(in); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "92999990001x", "999-0001", "+0123456789", "+55", "++5592999990001", "55 92 9999 0001 123456789", "+55;92999990001"} {
		if got, err := NormalizePhone(bad); err == nil {
			t.Errorf("%q must be refused, got %q", bad, got)
		}
	}
}

func TestNormalizeEmailAndParticipant(t *testing.T) {
	if got, err := NormalizeEmail("  Ana.Silva@K3G.com.BR "); err != nil || got != "ana.silva@k3g.com.br" {
		t.Errorf("email = %q %v", got, err)
	}
	for _, bad := range []string{"", "ana", "@k3g.com", "ana@", "a b@k3g.com", "a@b@c.com", "ana@k3g", "<a@k3g.com>"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if got, err := NormalizeParticipant(" 5592999990001@C.us "); err != nil || got != "5592999990001@c.us" {
		t.Errorf("participant = %q %v", got, err)
	}
	if _, err := NormalizeParticipant("a b"); err == nil {
		t.Error("spaces refused")
	}
}

func TestThereIsNoAISource(t *testing.T) {
	for _, s := range []VerificationSource{"ai", "llm", "inferred", ""} {
		if s.Valid() {
			t.Errorf("%q must not be a valid verification source", s)
		}
	}
	for _, s := range []VerificationSource{SourceAdmin, SourceProviderVerified, SourceChallenge, SourceImportVerified} {
		if !s.Valid() {
			t.Errorf("%q must be valid", s)
		}
	}
}
