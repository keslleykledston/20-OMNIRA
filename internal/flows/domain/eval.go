package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Builtin variables are provided read-only by the runtime; a flow can read but never assign them.
var builtinPaths = map[string]bool{
	"contact.id": true, "contact.name": true, "contact.phone": true, "contact.kind": true, "contact.classified": true,
	"customer.account_id": true, "customer.name": true, "customer.candidates_count": true,
	"tickets.count": true, "tickets.first_id": true, "tickets.first_subject": true,
	"conversation.id": true, "conversation.kind": true,
	"message.text": true, "channel.provider": true,
}

var builtinRoots = map[string]bool{"contact": true, "customer": true, "tickets": true, "conversation": true, "message": true, "channel": true}

func IsBuiltinPath(p string) bool     { return builtinPaths[p] }
func IsReservedRoot(name string) bool { return builtinRoots[name] }

// Lookup reads a variable by name or dotted path from the run variables.
func Lookup(vars map[string]any, path string) (any, bool) {
	var cur any = vars
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return fmt.Sprint(v)
}

// Interpolate replaces {{path}} with the variable's text ("" when unknown). Deterministic, no expressions, no code.
func Interpolate(text string, vars map[string]any) string {
	return templateRefExpr.ReplaceAllStringFunc(text, func(m string) string {
		path := templateRefExpr.FindStringSubmatch(m)[1]
		v, _ := Lookup(vars, path)
		return stringify(v)
	})
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func equalValues(a, b any) bool {
	if fa, ok := toFloat(a); ok {
		if fb, ok := toFloat(b); ok {
			return fa == fb
		}
	}
	return strings.EqualFold(stringify(a), stringify(b))
}

// EvalOp is the single deterministic comparison used by condition and switch. A comparison that does not make sense
// (e.g. gt on text) is false, never an error: a flow must not fail because a contact typed words.
func EvalOp(op string, left, right any, leftExists bool) bool {
	switch op {
	case "exists":
		return leftExists && stringify(left) != ""
	case "not_exists":
		return !leftExists || stringify(left) == ""
	case "eq":
		return leftExists && equalValues(left, right)
	case "neq":
		return !leftExists || !equalValues(left, right)
	case "contains":
		return leftExists && strings.Contains(strings.ToLower(stringify(left)), strings.ToLower(stringify(right)))
	case "in":
		list, ok := right.([]any)
		if !ok || !leftExists {
			return false
		}
		for _, item := range list {
			if equalValues(left, item) {
				return true
			}
		}
		return false
	case "gt", "gte", "lt", "lte":
		fl, ok1 := toFloat(left)
		fr, ok2 := toFloat(right)
		if !leftExists || !ok1 || !ok2 {
			return false
		}
		switch op {
		case "gt":
			return fl > fr
		case "gte":
			return fl >= fr
		case "lt":
			return fl < fr
		}
		return fl <= fr
	}
	return false
}
