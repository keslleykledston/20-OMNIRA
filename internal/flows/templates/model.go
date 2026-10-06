package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/flows/domain"
)

// Settings are the resolver attributes the installed flow starts with (the tenant may change them).
type Settings struct {
	Priority      int
	IsDefault     bool
	RestartPolicy domain.RestartPolicy
}

// Expect is the "Then" of a template test case (Given = Scenario, When = its events).
type Expect struct {
	Status      string            // final run status ("" = not checked)
	Reaches     []string          // node ids that must have run
	NotReaches  []string          // node ids that must NOT have run
	Say         []string          // substrings that must appear in the messages the bot sends, in order
	Effects     []string          // effect kinds expected, in order
	Priority    string            // priority of the (first) ticket the flow would open
	Vars        map[string]string // run variables that must equal these (as text)
	WaitingAt   string            // node the run must be waiting at
	NoMessages  bool
	NoEffects   bool
	TicketCount int // -1 = not checked
}

// TestCase is a Given/When/Then executed by the REAL simulator in the test suite (and exposed so authors can reuse it).
type TestCase struct {
	Name     string
	Scenario Scenario
	Expect   Expect
}

// Scenario mirrors application.Scenario without importing it (the application layer imports this package).
type Scenario struct {
	ContactName string
	ContactKind string // unclassified | customer | other
	Provider    string
	WindowOpen  *bool
	Companies   []string
	OpenTickets int
	Now         *time.Time // the clock the flow sees (business hours)
	Events      []Event
}

type Event struct {
	Timeout bool
	Text    string
}

func Msg(texts ...string) []Event {
	out := make([]Event, len(texts))
	for i, t := range texts {
		out[i] = Event{Text: t}
	}
	return out
}

// MappingDoc describes a placeholder the installer asks for.
type MappingDoc struct {
	Key         string // e.g. queue.technical
	Kind        string // queue
	Description string
	Required    bool
}

// Template is one immutable, versioned system template.
type Template struct {
	Slug           string
	Version        int
	Name           string
	Description    string
	Type           domain.FlowType
	Categories     []string
	Difficulty     string // starter | intermediate
	RecommendedFor []string
	Features       []string // features the tenant must have (ticketing...)
	Optional       []string // features that improve it (ai, monitoring...)
	Settings       Settings
	Definition     json.RawMessage
	Mappings       []MappingDoc
	Tests          []TestCase
	// Requires lists the SUBFLOW templates (by slug) this template calls; derived from the definition.
	Requires []string
}

// Hash is the identity of the published content: sha256 of the canonical definition + settings.
func (t *Template) Hash() string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s@%d|%s|%d|%t|%s|", t.Slug, t.Version, t.Type, t.Settings.Priority, t.Settings.IsDefault, t.Settings.RestartPolicy)))
	h.Write(t.Definition)
	return hex.EncodeToString(h.Sum(nil))
}

var refKey = regexp.MustCompile(`^[a-z]+\.[a-z0-9_]+$`)

// PlaceholderKeys returns the sorted placeholder keys a definition uses ({"$ref":"queue.technical"}).
func PlaceholderKeys(def json.RawMessage) []string {
	var raw any
	_ = json.Unmarshal(def, &raw)
	set := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok && len(x) == 1 {
				set[ref] = true
				return
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(raw)
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SubflowSlugs returns the slugs of the subflows a definition calls.
func SubflowSlugs(def json.RawMessage) []string {
	d, err := domain.ParseDefinition(def)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, n := range d.Nodes {
		if n.Type == domain.NodeSubflow {
			if c, err := domain.DecodeConfig[domain.SubflowConfig](n.Config); err == nil && c.Flow != "" {
				set[c.Flow] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Resolve substitutes every placeholder with the mapped resource id and rewrites subflow slugs (a pack may install a
// subflow under a different slug when the tenant already has one with that name). It returns the resolved definition.
func Resolve(def json.RawMessage, mapping map[string]string, subflowSlug map[string]string) (json.RawMessage, error) {
	var raw any
	if err := json.Unmarshal(def, &raw); err != nil {
		return nil, err
	}
	var missing []string
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok && len(x) == 1 {
				id, found := mapping[ref]
				if !found {
					missing = append(missing, ref)
					return v
				}
				return id
			}
			if flow, ok := x["flow"].(string); ok {
				if to, found := subflowSlug[flow]; found {
					x["flow"] = to
				}
			}
			for k, c := range x {
				x[k] = walk(c)
			}
			return x
		case []any:
			for i, c := range x {
				x[i] = walk(c)
			}
			return x
		}
		return v
	}
	resolved := walk(raw)
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing mapping for %s", strings.Join(missing, ", "))
	}
	return json.Marshal(resolved)
}

// Pack is a curated, versioned collection of templates for one kind of operation.
type Pack struct {
	Slug           string
	Version        int
	Name           string
	Description    string
	Categories     []string
	RecommendedFor []string
	Items          []PackItem
}

type PackItem struct {
	Template string
	Version  int
	Optional bool
}

func (p *Pack) Hash() string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s@%d|", p.Slug, p.Version)))
	for _, i := range p.Items {
		h.Write([]byte(fmt.Sprintf("%s@%d:%t;", i.Template, i.Version, i.Optional)))
	}
	return hex.EncodeToString(h.Sum(nil))
}
