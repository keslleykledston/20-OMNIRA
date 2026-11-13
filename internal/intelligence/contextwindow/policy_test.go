package contextwindow

import (
    "testing"
    "time"
)

func TestAdaptiveWindow(t *testing.T) {
    p:=DefaultPolicy()
    start:=time.Date(2026,11,13,14,0,0,0,time.UTC)
    s:=Snapshot{FirstAt:start,LastAt:start.Add(19*time.Second),Count:4}
    pending,err:=p.Evaluate(s,start.Add(45*time.Second),TriggerNone)
    if err!=nil || pending.Ready || pending.Reason!=ReasonPending || !pending.DueAt.Equal(start.Add(49*time.Second)) { t.Fatalf("pending %+v %v",pending,err) }
    flushed,err:=p.Evaluate(s,start.Add(49*time.Second),TriggerNone)
    if err!=nil || !flushed.Ready || flushed.Reason!=ReasonIdle { t.Fatalf("idle %+v %v",flushed,err) }
}

func TestHardMaximumAge(t *testing.T) {
    p:=DefaultPolicy()
    start:=time.Now().UTC()
    s:=Snapshot{FirstAt:start,LastAt:start.Add(115*time.Second),Count:11}
    pending,err:=p.Evaluate(s,start.Add(117*time.Second),TriggerNone)
    if err!=nil || pending.Ready || !pending.DueAt.Equal(start.Add(120*time.Second)) { t.Fatalf("deadline %+v %v",pending,err) }
    ready,err:=p.Evaluate(s,start.Add(120*time.Second),TriggerNone)
    if err!=nil || !ready.Ready || ready.Reason!=ReasonMaxAge { t.Fatalf("max age %+v %v",ready,err) }
}

func TestTriggersAndCapacity(t *testing.T) {
    p:=DefaultPolicy()
    start:=time.Now().UTC()
    s:=Snapshot{FirstAt:start,LastAt:start,Count:1}
    for _,tr:=range []Trigger{TriggerManual,TriggerHandoff,TriggerClaim,TriggerFinalize,TriggerPriority} {
        d,err:=p.Evaluate(s,start.Add(time.Second),tr)
        if err!=nil || !d.Ready || d.Reason!=ReasonForced { t.Fatalf("%s: %+v %v",tr,d,err) }
    }
    s.Count=12
    d,err:=p.Evaluate(s,start,TriggerNone)
    if err!=nil || !d.Ready || d.Reason!=ReasonMaxMessages { t.Fatalf("capacity %+v %v",d,err) }
}

func TestInvalidInputs(t *testing.T) {
    p:=DefaultPolicy()
    start:=time.Now().UTC()
    for _,s:=range []Snapshot{{},{FirstAt:start,LastAt:start.Add(-time.Second),Count:1},{FirstAt:start,LastAt:start,Count:0}} {
        if _,err:=p.Evaluate(s,start,TriggerNone); err==nil {t.Fatalf("accepted: %+v",s)}
    }
    if _,err:=p.Evaluate(Snapshot{FirstAt:start,LastAt:start,Count:1},start.Add(-time.Second),TriggerNone); err==nil {t.Fatal("accepted past now")}
    if _,err:=p.Evaluate(Snapshot{FirstAt:start,LastAt:start,Count:1},start,"unknown"); err==nil {t.Fatal("accepted unknown trigger")}
    if (Policy{Idle:3*time.Minute,MaxAge:time.Minute,MaxMessages:12}).Validate()==nil {t.Fatal("accepted invalid policy")}
}
