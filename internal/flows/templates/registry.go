package templates

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is the immutable in-memory catalog of system templates and packs.
type Registry struct {
	templates map[string]map[int]*Template
	packs     map[string]map[int]*Pack
}

var (
	once sync.Once
	def  *Registry
	derr error
)

// Default returns the built-in registry. Building it validates the library (every template must build and every pack item
// must resolve), so a broken library fails fast and loudly in the test suite and at startup.
func Default() (*Registry, error) {
	once.Do(func() { def, derr = build() })
	return def, derr
}

func build() (*Registry, error) {
	r := &Registry{templates: map[string]map[int]*Template{}, packs: map[string]map[int]*Pack{}}
	for _, group := range [][]*Template{starterTemplates(), k3gTemplates(), ispTemplates()} {
		for _, t := range group {
			if t == nil {
				continue
			}
			if t.Definition == nil {
				return nil, fmt.Errorf("template %q has no definition (builder error)", t.Slug)
			}
			if r.templates[t.Slug] == nil {
				r.templates[t.Slug] = map[int]*Template{}
			}
			if _, dup := r.templates[t.Slug][t.Version]; dup {
				return nil, fmt.Errorf("template %s@%d is defined twice", t.Slug, t.Version)
			}
			r.templates[t.Slug][t.Version] = t
		}
	}
	for _, p := range allPacks() {
		if r.packs[p.Slug] == nil {
			r.packs[p.Slug] = map[int]*Pack{}
		}
		if _, dup := r.packs[p.Slug][p.Version]; dup {
			return nil, fmt.Errorf("pack %s@%d is defined twice", p.Slug, p.Version)
		}
		for _, it := range p.Items {
			if _, ok := r.Template(it.Template, it.Version); !ok {
				return nil, fmt.Errorf("pack %s@%d references missing template %s@%d", p.Slug, p.Version, it.Template, it.Version)
			}
		}
		r.packs[p.Slug][p.Version] = p
	}
	return r, nil
}

// Template returns slug@version; version 0 means the latest published one.
func (r *Registry) Template(slug string, version int) (*Template, bool) {
	vs := r.templates[slug]
	if len(vs) == 0 {
		return nil, false
	}
	if version == 0 {
		for v := range vs {
			if v > version {
				version = v
			}
		}
	}
	t, ok := vs[version]
	return t, ok
}

// Templates lists the latest version of every template, sorted by slug.
func (r *Registry) Templates() []*Template {
	out := make([]*Template, 0, len(r.templates))
	for slug := range r.templates {
		t, _ := r.Template(slug, 0)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// AllVersions lists every published (slug, version), for the immutability guard.
func (r *Registry) AllVersions() []*Template {
	var out []*Template
	for _, vs := range r.templates {
		for _, t := range vs {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		return out[i].Version < out[j].Version
	})
	return out
}

func (r *Registry) Pack(slug string, version int) (*Pack, bool) {
	vs := r.packs[slug]
	if len(vs) == 0 {
		return nil, false
	}
	if version == 0 {
		for v := range vs {
			if v > version {
				version = v
			}
		}
	}
	p, ok := vs[version]
	return p, ok
}

func (r *Registry) Packs() []*Pack {
	out := make([]*Pack, 0, len(r.packs))
	for slug := range r.packs {
		p, _ := r.Pack(slug, 0)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

func (r *Registry) AllPackVersions() []*Pack {
	var out []*Pack
	for _, vs := range r.packs {
		for _, p := range vs {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// Closure returns the templates to install for the chosen items plus every subflow they call (transitively), ordered so a
// dependency always comes BEFORE the template that calls it. selected nil = all non-optional items.
func (r *Registry) Closure(p *Pack, selected map[string]bool) ([]*Template, error) {
	var ordered []*Template
	seen := map[string]bool{}
	var visit func(slug string, version int, stack []string) error
	visit = func(slug string, version int, stack []string) error {
		for _, s := range stack {
			if s == slug {
				return fmt.Errorf("template dependency cycle through %q", slug)
			}
		}
		if seen[slug] {
			return nil
		}
		t, ok := r.Template(slug, version)
		if !ok {
			return fmt.Errorf("template %q is not in the library", slug)
		}
		for _, dep := range t.Requires {
			if err := visit(dep, 0, append(stack, slug)); err != nil {
				return err
			}
		}
		seen[slug] = true
		ordered = append(ordered, t)
		return nil
	}
	for _, it := range p.Items {
		if selected == nil && it.Optional {
			continue
		}
		if selected != nil && !selected[it.Template] {
			continue
		}
		if err := visit(it.Template, it.Version, nil); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// Mappings aggregates the placeholders of a set of templates ONCE (a queue shared by ten templates is asked a single time).
func Mappings(ts []*Template) []MappingDoc {
	seen := map[string]MappingDoc{}
	for _, t := range ts {
		for _, m := range t.Mappings {
			if _, ok := seen[m.Key]; !ok {
				seen[m.Key] = m
			}
		}
	}
	out := make([]MappingDoc, 0, len(seen))
	for _, m := range seen {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
