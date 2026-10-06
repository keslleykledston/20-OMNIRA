package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeTrimsAndDefaultsTheTruth(t *testing.T) {
	in := FinalizeInput{ConversationID: uuid.New(), Reason: ReasonResolved, Note: "  ok  ", Summary: "  resumo  ",
		FollowUps: []FollowUpInput{{Kind: KindPending, Text: "  fazer  "}}}
	out, err := in.Normalize()
	if err != nil || out.Note != "ok" || out.Summary != "resumo" || out.FollowUps[0].Text != "fazer" || out.SummaryTruth != TruthAgentConfirmed {
		t.Fatalf("%+v %v", out, err)
	}
	if in.Note != "  ok  " || in.FollowUps[0].Text != "  fazer  " {
		t.Fatal("Normalize must not mutate its receiver")
	}
}

func TestNormalizeRefusals(t *testing.T) {
	id := uuid.New()
	long := func(n int) string { return strings.Repeat("a", n) }
	for name, in := range map[string]FinalizeInput{
		"no conversation":    {Reason: ReasonResolved},
		"bad reason":         {ConversationID: id, Reason: "x"},
		"bad truth":          {ConversationID: id, Reason: ReasonOther, SummaryTruth: "system_verified"},
		"long note":          {ConversationID: id, Reason: ReasonOther, Note: long(MaxNote + 1)},
		"long summary":       {ConversationID: id, Reason: ReasonOther, Summary: long(MaxSummary + 1)},
		"private key":        {ConversationID: id, Reason: ReasonOther, Note: "-----BEGIN RSA PRIVATE KEY-----"},
		"aws key":            {ConversationID: id, Reason: ReasonOther, Summary: "AKIAABCDEFGHIJKLMNOP"},
		"blank follow-up":    {ConversationID: id, Reason: ReasonOther, FollowUps: []FollowUpInput{{Kind: KindInfo, Text: " "}}},
		"long follow-up":     {ConversationID: id, Reason: ReasonOther, FollowUps: []FollowUpInput{{Kind: KindInfo, Text: long(MaxFollowUp + 1)}}},
		"credential in item": {ConversationID: id, Reason: ReasonOther, FollowUps: []FollowUpInput{{Kind: KindInfo, Text: "bearer abcdefghij1234567"}}},
		"invalid UTF-8":      {ConversationID: id, Reason: ReasonOther, Note: "a\xffb"},
	} {
		if _, err := in.Normalize(); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	if len(FinalizeInput{}.FollowUps) != 0 {
		t.Fatal("sanity")
	}
	many := make([]FollowUpInput, MaxFollowUps+1)
	for i := range many {
		many[i] = FollowUpInput{Kind: KindInfo, Text: "x"}
	}
	if _, err := (FinalizeInput{ConversationID: id, Reason: ReasonOther, FollowUps: many}).Normalize(); err == nil {
		t.Error("more than the maximum items must be refused")
	}
}

func TestResolveInput(t *testing.T) {
	for _, st := range []FollowUpStatus{StatusDone, StatusDropped} {
		if _, err := (ResolveFollowUpInput{Status: st, Note: " ok "}).Normalize(); err != nil {
			t.Errorf("%s: %v", st, err)
		}
	}
	if _, err := (ResolveFollowUpInput{Status: StatusOpen}).Normalize(); err == nil {
		t.Error("open is not a resolution")
	}
	if _, err := (ResolveFollowUpInput{Status: StatusDone, Note: "Bearer abcdefghijk123"}).Normalize(); err == nil {
		t.Error("credentials in the note are refused")
	}
}
