// Package contextdelta strictly validates model-generated context proposals.
// These are untrusted, non-operational suggestions, never evidence that an
// external action was executed or that a ticket was resolved.
package contextdelta

import (
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "strings"
    "unicode/utf8"
)

type Kind string

const (
    Need Kind = "need"
    Observation Kind = "observation"
    Commitment Kind = "commitment"
    SuggestedAction Kind = "suggested_action"
    PossibleResolution Kind = "possible_resolution"
    Risk Kind = "risk"
)

type Fact struct {
    TopicAlias string `json:"topic_alias"`
    Kind Kind `json:"kind"`
    Text string `json:"text"`
    Evidence []string `json:"evidence"`
}

type Proposal struct {
    Summary string `json:"summary"`
    Facts []Fact `json:"facts"`
}

// Scope is constructed exclusively from persisted, authorized rows.
// The model can cite aliases but must never choose database IDs or tenant IDs.
type Scope struct {
    EvidenceAliases map[string]bool
    TopicAliases map[string]bool
}

const (
    MaxJSONBytes = 32 << 10
    MaxSummaryRunes = 2000
    MaxFacts = 20
    MaxFactRunes = 500
    MaxEvidence = 8
)

var ErrInvalidProposal = errors.New("contextdelta: invalid model proposal")

func Parse(raw string, scope Scope) (Proposal, error) {
    if len(raw)==0 || len(raw)>MaxJSONBytes {
        return Proposal{}, fmt.Errorf("%w: invalid size",ErrInvalidProposal)
    }
    dec:=json.NewDecoder(strings.NewReader(raw))
    dec.DisallowUnknownFields()
    var out Proposal
    if err:=dec.Decode(&out); err!=nil {return Proposal{},fmt.Errorf("%w: %v",ErrInvalidProposal,err)}
    var extra any
    if err:=dec.Decode(&extra); err!=io.EOF {return Proposal{},fmt.Errorf("%w: trailing data",ErrInvalidProposal)}
    out.Summary=strings.TrimSpace(out.Summary)
    if !validText(out.Summary,MaxSummaryRunes) || len(out.Facts)>MaxFacts {return Proposal{},fmt.Errorf("%w: summary or count",ErrInvalidProposal)}
    for i:=range out.Facts {
        f:=&out.Facts[i]
        f.Text=strings.TrimSpace(f.Text)
        if !scope.TopicAliases[f.TopicAlias] || !validKind(f.Kind) || !validText(f.Text,MaxFactRunes) ||
            len(f.Evidence)==0 || len(f.Evidence)>MaxEvidence {
            return Proposal{},fmt.Errorf("%w: fact %d",ErrInvalidProposal,i)
        }
        seen:=map[string]bool{}
        for _,alias:=range f.Evidence {
            if !scope.EvidenceAliases[alias] || seen[alias] {return Proposal{},fmt.Errorf("%w: evidence %d",ErrInvalidProposal,i)}
            seen[alias]=true
        }
    }
    return out,nil
}

func validKind(k Kind) bool {
    switch k {case Need,Observation,Commitment,SuggestedAction,PossibleResolution,Risk: return true}
    return false
}
func validText(s string,max int) bool {return s!="" && utf8.ValidString(s) && utf8.RuneCountInString(s)<=max}
