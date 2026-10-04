package domain

import (
	"strings"
	"testing"
	"time"
)

func TestHandoffTokensAreOpaqueUniqueAndOnlyTheirHashIsStored(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, hash, err := NewHandoffToken()
		if err != nil || !strings.HasPrefix(tok, HandoffTokenPrefix) || len(tok) != len(HandoffTokenPrefix)+43 {
			t.Fatalf("token %q %v", tok, err)
		}
		if seen[tok] || seen[hash] {
			t.Fatal("tokens must be unique")
		}
		seen[tok], seen[hash] = true, true
		if len(hash) != 64 || hash != HashHandoffToken(tok) || strings.Contains(hash, tok) || hash == tok {
			t.Fatalf("hash = %q", hash)
		}
	}
}

func TestFindHandoffTokenReadsOnlyTheShape(t *testing.T) {
	tok, _, _ := NewHandoffToken()
	for _, in := range []string{tok, "oi, aqui está " + tok + " obrigado", "\n" + tok + "\n"} {
		if got, ok := FindHandoffToken(in); !ok || got != tok {
			t.Errorf("%q -> %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "omn-curto", "omn-" + strings.Repeat("!", 43), "meu pedido 837", strings.Repeat("a", 5000)} {
		if _, ok := FindHandoffToken(in); ok {
			t.Errorf("%q must not look like a token", in)
		}
	}
}

func TestHandoffTTLAndEffectiveStatus(t *testing.T) {
	if ClampHandoffTTL(0) != DefaultHandoffTTL || ClampHandoffTTL(-time.Hour) != DefaultHandoffTTL || ClampHandoffTTL(500*time.Hour) != MaxHandoffTTL || ClampHandoffTTL(time.Hour) != time.Hour {
		t.Error("ttl clamp")
	}
	now := time.Now()
	h := TopicHandoff{Status: HandoffPending, ExpiresAt: now.Add(time.Minute)}
	if h.EffectiveStatus(now) != "pending" || h.EffectiveStatus(now.Add(time.Hour)) != "expired" {
		t.Error("pending/expired")
	}
	h.Status = HandoffRedeemed
	if h.EffectiveStatus(now.Add(time.Hour)) != "redeemed" {
		t.Error("a redeemed invitation never reads as expired")
	}
}
