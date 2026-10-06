package application

import (
	"fmt"

	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

func aiCalls(vars map[string]any) int {
	switch n := vars["_ai_calls"].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// aiFailure sends the run down the node's mandatory `error` port (the validator forces it to be connected): the AI being off,
// slow, wrong or over budget is an expected outcome with a deterministic fallback, never a failed run.
func aiFailure(reason string, calls int) StepResult {
	return StepResult{Port: "error", Reserved: map[string]any{"_ai_calls": float64(calls)}, Output: map[string]any{"error": truncate(reason, 200)}}
}

func (r aiGuard) allow(in StepInput) (calls int, ok bool, reason string) {
	if r.gw == nil {
		return 0, false, "ai is not configured"
	}
	calls = aiCalls(in.Vars)
	if calls >= domain.MaxAICallsPerRun {
		return calls, false, fmt.Sprintf("the run reached the limit of %d AI calls", domain.MaxAICallsPerRun)
	}
	return calls, true, ""
}

type aiGuard struct{ gw ports.AIGateway }

func messageText(in StepInput) string {
	v, _ := domain.Lookup(in.Vars, "message.text")
	s, _ := v.(string)
	return s
}

type aiClassifyExec struct{ aiGuard }

func (aiClassifyExec) Type() domain.NodeType { return domain.NodeAIClassify }
func (e aiClassifyExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.AIClassifyConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	calls, ok, why := e.allow(in)
	if !ok {
		return aiFailure(why, calls), nil
	}
	text := messageText(in)
	if text == "" {
		return StepResult{Port: "low_confidence", Reserved: map[string]any{"_ai_calls": float64(calls)}, Output: map[string]any{"reason": "empty message"}}, nil
	}
	res, err := e.gw.Classify(in.Ctx, in.Facts.ID, text, c.Intents)
	if err != nil {
		return aiFailure(err.Error(), calls+1), nil
	}
	min := c.MinConfidence
	if min == 0 {
		min = domain.DefaultAIMinConfidence
	}
	reserved := map[string]any{"ai": map[string]any{"intent": res.IntentID, "confidence": res.Confidence}, "_ai_calls": float64(calls + 1)}
	out := map[string]any{"intent": res.IntentID, "confidence": res.Confidence, "threshold": min}
	if res.Confidence < min {
		return StepResult{Port: "low_confidence", Reserved: reserved, Output: out}, nil
	}
	return StepResult{Port: res.IntentID, Reserved: reserved, Output: out}, nil
}

type aiExtractExec struct{ aiGuard }

func (aiExtractExec) Type() domain.NodeType { return domain.NodeAIExtract }
func (e aiExtractExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.AIExtractConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	calls, ok, why := e.allow(in)
	if !ok {
		return aiFailure(why, calls), nil
	}
	got, err := e.gw.Extract(in.Ctx, in.Facts.ID, messageText(in), c.Fields)
	if err != nil {
		return aiFailure(err.Error(), calls+1), nil
	}
	set := map[string]any{}
	for k, v := range ValidateExtracted(toAnyMap(got), c.Fields) { // never trust the gateway to have validated
		set[k] = v
	}
	return StepResult{Port: "next", SetVars: set, Reserved: map[string]any{"_ai_calls": float64(calls + 1)}, Output: map[string]any{"extracted": len(set)}}, nil
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

type aiSummarizeExec struct{ aiGuard }

func (aiSummarizeExec) Type() domain.NodeType { return domain.NodeAISummarize }
func (e aiSummarizeExec) Execute(in StepInput) (StepResult, error) {
	c, err := domain.DecodeConfig[domain.AISummarizeConfig](in.Node.Config)
	if err != nil {
		return StepResult{}, err
	}
	calls, ok, why := e.allow(in)
	if !ok {
		return aiFailure(why, calls), nil
	}
	sum, err := e.gw.Summarize(in.Ctx, in.Facts.ID, c.MaxMessages)
	if err != nil {
		return aiFailure(err.Error(), calls+1), nil
	}
	sum, _ = domain.Redact(sum).(string)
	return StepResult{Port: "next", SetVars: map[string]any{c.Variable: sum}, Reserved: map[string]any{"_ai_calls": float64(calls + 1)}, Output: map[string]any{"summary_chars": len([]rune(sum))}}, nil
}

// AIExecutors are the AI nodes bound to a gateway; with a nil gateway they all take their `error` port.
func AIExecutors(gw ports.AIGateway) []Executor {
	g := aiGuard{gw: gw}
	return []Executor{aiClassifyExec{g}, aiExtractExec{g}, aiSummarizeExec{g}}
}

// AllExecutorsWith is the full catalog with the given AI gateway.
func AllExecutorsWith(gw ports.AIGateway) []Executor {
	return append(append(PureExecutors(), DataExecutors()...), AIExecutors(gw)...)
}
