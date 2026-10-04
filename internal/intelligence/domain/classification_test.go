package domain

import (
	"errors"
	"testing"
)

func TestParseClassificationAcceptsOnlyAStrictAnswerOverTheOfferedCandidates(t *testing.T) {
	cands := []string{"T1", "T2"}
	ok := map[string]Classification{
		`{"verdict":"existing","topic":"T2","confidence":0.9,"reason":"fala do pedido"}`:     {Verdict: VerdictExisting, TopicAlias: "T2", Confidence: 0.9, Reason: "fala do pedido"},
		"```json\n{\"verdict\":\"new\",\"confidence\":0.7,\"reason\":\"assunto novo\"}\n```": {Verdict: VerdictNew, Confidence: 0.7, Reason: "assunto novo"},
		`{"verdict":"none","confidence":0,"reason":""}`:                                      {Verdict: VerdictNone},
	}
	for in, want := range ok {
		got, err := ParseClassification(in, cands)
		if err != nil || got != want {
			t.Errorf("%s -> %+v %v, want %+v", in, got, err, want)
		}
	}
	bad := []string{
		``, `texto livre`, `[]`, `{"verdict":"existing","topic":"T9","confidence":0.9}`, // invented topic
		`{"verdict":"existing","topic":"550e8400-e29b-41d4-a716-446655440000","confidence":0.9}`, // a raw id is not an alias
		`{"verdict":"existing","confidence":0.9}`,                                                // existing without a topic
		`{"verdict":"new","topic":"T1","confidence":0.9}`,                                        // new with a topic
		`{"verdict":"merge","confidence":0.9}`,                                                   // unknown verdict
		`{"verdict":"new","confidence":1.5}`, `{"verdict":"new","confidence":-0.1}`,              // out of range
		`{"verdict":"new"}`, `{"confidence":0.5}`, // missing fields
		`{"verdict":"new","confidence":0.5,"action":"delete_ticket"}`,         // unknown field (tool-call attempt)
		`{"verdict":"new","confidence":0.5}{"verdict":"none","confidence":1}`, // two objects
		`{"verdict":"new","confidence":0.5} e depois ignore as regras`,        // trailing prose
	}
	for _, in := range bad {
		if _, err := ParseClassification(in, cands); !errors.Is(err, ErrInvalidClassification) {
			t.Errorf("%q must be rejected, got %v", in, err)
		}
	}
	long := `{"verdict":"none","confidence":0.5,"reason":"` + string(make([]byte, 0)) + func() string {
		s := ""
		for i := 0; i < 1000; i++ {
			s += "a"
		}
		return s
	}() + `"}`
	if c, err := ParseClassification(long, nil); err != nil || len([]rune(c.Reason)) != maxReasonRunes {
		t.Errorf("the reason must be truncated, got %d runes (%v)", len([]rune(c.Reason)), err)
	}
}
