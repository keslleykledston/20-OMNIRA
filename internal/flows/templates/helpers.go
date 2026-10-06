package templates

import (
	"fmt"

	"github.com/omnira/omnira/internal/flows/domain"
)

var mappingDocs = map[string]MappingDoc{
	"queue.technical":  {Key: "queue.technical", Kind: "queue", Description: "Fila do suporte técnico", Required: true},
	"queue.noc":        {Key: "queue.noc", Kind: "queue", Description: "Fila do NOC (incidentes de rede)", Required: true},
	"queue.noc_l2l3":   {Key: "queue.noc_l2l3", Kind: "queue", Description: "Fila do NOC L2/L3 (BGP, roteamento, peering)", Required: true},
	"queue.finance":    {Key: "queue.finance", Kind: "queue", Description: "Fila do financeiro", Required: true},
	"queue.commercial": {Key: "queue.commercial", Kind: "queue", Description: "Fila do comercial", Required: true},
	"queue.projects":   {Key: "queue.projects", Kind: "queue", Description: "Fila de projetos / implantação", Required: true},
	"queue.fallback":   {Key: "queue.fallback", Kind: "queue", Description: "Fila de triagem humana: para onde o atendimento vai quando o bot não resolve", Required: true},
}

// mk assembles a Template from a builder: it derives the placeholder documentation and the subflow dependencies from the
// definition itself, so they can never drift from what the flow really uses.
func mk(slug string, version int, name, description string, typ domain.FlowType, cats []string, st Settings, b *Builder, tests []TestCase, extra ...func(*Template)) *Template {
	raw, err := b.Build()
	if err != nil {
		panic(fmt.Sprintf("template %s: %v", slug, err)) // a programming error in the library; caught by the first test run
	}
	if st.RestartPolicy == "" {
		st.RestartPolicy = domain.RestartNewConversationOnly
	}
	if st.Priority == 0 {
		st.Priority = 100
	}
	t := &Template{Slug: slug, Version: version, Name: name, Description: description, Type: typ, Categories: cats, Difficulty: "starter",
		Features: []string{"conversations"}, Settings: st, Definition: raw, Tests: tests, Requires: SubflowSlugs(raw)}
	for _, key := range PlaceholderKeys(raw) {
		doc, ok := mappingDocs[key]
		if !ok {
			panic(fmt.Sprintf("template %s uses undocumented placeholder %q", slug, key))
		}
		t.Mappings = append(t.Mappings, doc)
	}
	for _, f := range extra {
		f(t)
	}
	return t
}

func withFeatures(required, optional []string) func(*Template) {
	return func(t *Template) { t.Features, t.Optional = required, optional }
}
func recommended(who ...string) func(*Template) { return func(t *Template) { t.RecommendedFor = who } }
func intermediate() func(*Template)             { return func(t *Template) { t.Difficulty = "intermediate" } }

func boolp(b bool) *bool { return &b }
