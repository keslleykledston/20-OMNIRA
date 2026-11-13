package contextdelta

import (
    "errors"
    "strings"
    "testing"
)

var scope=Scope{
    EvidenceAliases:map[string]bool{"M1":true,"M2":true,"EVENT1":true},
    TopicAliases:map[string]bool{"T1":true,"NEW":true},
}

func TestParseEvidenceBacked(t *testing.T) {
    v,err:=Parse(`{"summary":"Cliente solicitou suporte e boleto","facts":[{"topic_alias":"T1","kind":"need","text":"Instabilidade","evidence":["M1"]},{"topic_alias":"NEW","kind":"possible_resolution","text":"Cliente relatou retorno","evidence":["M2"]}]}`,scope)
    if err!=nil || len(v.Facts)!=2 || v.Facts[1].Kind!=PossibleResolution {t.Fatalf("got %+v %v",v,err)}
}

func TestRejectFabricationAndExecution(t *testing.T) {
    bad:=[]string{
        `{"summary":"x","facts":[{"topic_alias":"T1","kind":"need","text":"x","evidence":["M999"]}]}`,
        `{"summary":"x","facts":[{"topic_alias":"T2","kind":"need","text":"x","evidence":["M1"]}]}`,
        `{"summary":"x","facts":[{"topic_alias":"T1","kind":"resolved","text":"x","evidence":["M1"]}]}`,
        `{"summary":"x","facts":[{"topic_alias":"T1","kind":"need","text":"x","evidence":["M1","M1"]}]}`,
        `{"summary":"x","facts":[{"topic_alias":"T1","kind":"need","text":"x","evidence":[],"execute":true}]}`,
        `{"summary":"x","facts":[]} {"summary":"y"}`,
        `{"summary":"","facts":[]}`,
        `{"summary":"`+strings.Repeat("A",MaxSummaryRunes+1)+`","facts":[]}`,
    }
    for i,raw:=range bad {
        if _,err:=Parse(raw,scope); !errors.Is(err,ErrInvalidProposal) {t.Fatalf("case %d must fail: %v",i,err)}
    }
}

func TestNoImplicitResolutionAuthority(t *testing.T) {
    v,err:=Parse(`{"summary":"Cliente diz que normalizou","facts":[{"topic_alias":"T1","kind":"possible_resolution","text":"Possível resolução","evidence":["M1"]}]}`,scope)
    if err!=nil || v.Facts[0].Kind!=PossibleResolution {t.Fatalf("unexpected: %+v %v",v,err)}
}
