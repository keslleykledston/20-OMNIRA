package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
	"github.com/omnira/omnira/internal/flows/templates"
)

const (
	AuditTemplateInstalled = "flow_template.installed"
	AuditPackInstalled     = "flow_pack.installed"
)

// TemplateService installs system templates and packs as TENANT-OWNED DRAFTS. The installed flow carries provenance (slug,
// version, when) and nothing else: it has no runtime link to the template, a later template version never changes it, and
// nothing is ever published automatically.
type TemplateService struct {
	reg       *templates.Registry
	repo      ports.FlowRepository
	cp        *ControlPlane
	resources ports.ResourceChecker
	atomic    ports.Atomic
	audit     ports.Auditor
}

func NewTemplateService(reg *templates.Registry, repo ports.FlowRepository, cp *ControlPlane, resources ports.ResourceChecker, atomic ports.Atomic, audit ports.Auditor) *TemplateService {
	return &TemplateService{reg: reg, repo: repo, cp: cp, resources: resources, atomic: atomic, audit: audit}
}

// ---- catalog views -------------------------------------------------------------------------------------------------

type TemplateInfo struct {
	Slug           string          `json:"slug"`
	Version        int             `json:"version"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Type           string          `json:"type"`
	Categories     []string        `json:"categories"`
	Difficulty     string          `json:"difficulty"`
	RecommendedFor []string        `json:"recommended_for"`
	Features       []string        `json:"required_features"`
	Optional       []string        `json:"optional_features"`
	Mappings       []MappingInfo   `json:"mappings"`
	Requires       []string        `json:"requires_subflows"`
	TestCases      int             `json:"test_cases"`
	Settings       TemplateSetting `json:"settings"`
	Hash           string          `json:"hash"`
	Definition     json.RawMessage `json:"definition,omitempty"` // only in the detail (preview) view
}

type TemplateSetting struct {
	Priority      int    `json:"priority"`
	IsDefault     bool   `json:"is_default"`
	RestartPolicy string `json:"restart_policy"`
}

type MappingInfo struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

func mappingInfos(ms []templates.MappingDoc) []MappingInfo {
	out := make([]MappingInfo, 0, len(ms))
	for _, m := range ms {
		out = append(out, MappingInfo{Key: m.Key, Kind: m.Kind, Description: m.Description, Required: m.Required})
	}
	return out
}

func infoOf(t *templates.Template, withDefinition bool) TemplateInfo {
	i := TemplateInfo{Slug: t.Slug, Version: t.Version, Name: t.Name, Description: t.Description, Type: string(t.Type), Categories: t.Categories, Difficulty: t.Difficulty,
		RecommendedFor: t.RecommendedFor, Features: t.Features, Optional: t.Optional, Mappings: mappingInfos(t.Mappings), Requires: t.Requires, TestCases: len(t.Tests),
		Settings: TemplateSetting{Priority: t.Settings.Priority, IsDefault: t.Settings.IsDefault, RestartPolicy: string(t.Settings.RestartPolicy)}, Hash: t.Hash()}
	if i.Categories == nil {
		i.Categories = []string{}
	}
	if withDefinition {
		i.Definition = t.Definition
	}
	return i
}

type TemplateFilter struct{ Category, Query, RecommendedFor string }

func (s *TemplateService) Templates(f TemplateFilter) []TemplateInfo {
	out := []TemplateInfo{}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	for _, t := range s.reg.Templates() {
		if f.Category != "" && !contains(t.Categories, strings.ToUpper(f.Category)) {
			continue
		}
		if f.RecommendedFor != "" && !contains(t.RecommendedFor, strings.ToLower(f.RecommendedFor)) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(t.Name+" "+t.Description+" "+t.Slug), q) {
			continue
		}
		out = append(out, infoOf(t, false))
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *TemplateService) Template(slug string, version int) (*TemplateInfo, error) {
	t, ok := s.reg.Template(slug, version)
	if !ok {
		return nil, domain.ErrNotFound
	}
	i := infoOf(t, true)
	return &i, nil
}

type PackItemInfo struct {
	Template string `json:"template"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Version  int    `json:"version"`
	Optional bool   `json:"optional"`
	// Dependency is true for a subflow pulled in only because a selected template calls it.
	Dependency bool `json:"dependency"`
}

type PackInfo struct {
	Slug           string         `json:"slug"`
	Version        int            `json:"version"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Categories     []string       `json:"categories"`
	RecommendedFor []string       `json:"recommended_for"`
	Items          []PackItemInfo `json:"items"`
	// Mappings are the placeholders of what would be installed, each asked ONCE however many templates use it.
	Mappings []MappingInfo `json:"mappings"`
	Features []string      `json:"required_features"`
	Optional []string      `json:"optional_features"`
	Hash     string        `json:"hash"`
}

func (s *TemplateService) packInfo(p *templates.Pack, selected map[string]bool) (*PackInfo, error) {
	closure, err := s.reg.Closure(p, selected)
	if err != nil {
		return nil, err
	}
	chosen := map[string]bool{}
	for _, it := range p.Items {
		chosen[it.Template] = true
	}
	info := &PackInfo{Slug: p.Slug, Version: p.Version, Name: p.Name, Description: p.Description, Categories: p.Categories, RecommendedFor: p.RecommendedFor, Hash: p.Hash()}
	for _, it := range p.Items {
		t, _ := s.reg.Template(it.Template, it.Version)
		info.Items = append(info.Items, PackItemInfo{Template: it.Template, Name: t.Name, Type: string(t.Type), Version: it.Version, Optional: it.Optional})
	}
	feat, opt := map[string]bool{}, map[string]bool{}
	for _, t := range closure {
		for _, f := range t.Features {
			feat[f] = true
		}
		for _, f := range t.Optional {
			opt[f] = true
		}
		if !chosen[t.Slug] {
			info.Items = append(info.Items, PackItemInfo{Template: t.Slug, Name: t.Name, Type: string(t.Type), Version: t.Version, Dependency: true})
		}
	}
	info.Mappings = mappingInfos(templates.Mappings(closure))
	info.Features, info.Optional = sortedKeys(feat), sortedKeys(opt)
	return info, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *TemplateService) Packs(recommendedFor string) ([]PackInfo, error) {
	out := []PackInfo{}
	for _, p := range s.reg.Packs() {
		if recommendedFor != "" && !contains(p.RecommendedFor, strings.ToLower(recommendedFor)) {
			continue
		}
		i, err := s.packInfo(p, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, nil
}

// Pack previews a pack for the chosen templates (selected nil = every non-optional one).
func (s *TemplateService) Pack(slug string, version int, selected []string) (*PackInfo, error) {
	p, ok := s.reg.Pack(slug, version)
	if !ok {
		return nil, domain.ErrNotFound
	}
	sel, err := s.selection(p, selected)
	if err != nil {
		return nil, err
	}
	return s.packInfo(p, sel)
}

func (s *TemplateService) selection(p *templates.Pack, selected []string) (map[string]bool, error) {
	if selected == nil {
		return nil, nil
	}
	in := map[string]bool{}
	for _, it := range p.Items {
		in[it.Template] = true
	}
	sel := map[string]bool{}
	for _, slug := range selected {
		if !in[slug] {
			return nil, fmt.Errorf("%w: %q is not part of pack %s", domain.ErrInvalid, slug, p.Slug)
		}
		sel[slug] = true
	}
	return sel, nil
}

// ---- installation --------------------------------------------------------------------------------------------------

type InstallRequest struct {
	Pack      string            // pack slug, or "" to install one template
	Version   int               // 0 = latest
	Templates []string          // pack: chosen items (nil = all non-optional); single: the template slug
	Mappings  map[string]string // placeholder key -> id of the tenant's own resource
}

type InstalledFlow struct {
	FlowID          uuid.UUID      `json:"flow_id"`
	Slug            string         `json:"slug"`
	Template        string         `json:"template"`
	TemplateVersion int            `json:"template_version"`
	IsDefault       bool           `json:"is_default"`
	Dependency      bool           `json:"dependency"`
	Issues          []domain.Issue `json:"issues,omitempty"`
}

type InstallResult struct {
	PackInstallationID *uuid.UUID      `json:"pack_installation_id,omitempty"`
	Flows              []InstalledFlow `json:"flows"`
	Notes              []string        `json:"notes,omitempty"`
}

// MissingMappingsError lists what the administrator still has to map; nothing was written.
type MissingMappingsError struct{ Keys []string }

func (e *MissingMappingsError) Error() string {
	return "missing mappings: " + strings.Join(e.Keys, ", ")
}
func (e *MissingMappingsError) Is(t error) bool { return t == domain.ErrInvalid }

func (s *TemplateService) closureFor(req InstallRequest) ([]*templates.Template, *templates.Pack, map[string]bool, error) {
	if req.Pack == "" {
		if len(req.Templates) != 1 {
			return nil, nil, nil, fmt.Errorf("%w: choose exactly one template", domain.ErrInvalid)
		}
		t, ok := s.reg.Template(req.Templates[0], req.Version)
		if !ok {
			return nil, nil, nil, domain.ErrNotFound
		}
		virtual := &templates.Pack{Slug: "", Items: []templates.PackItem{{Template: t.Slug, Version: t.Version}}}
		c, err := s.reg.Closure(virtual, nil)
		return c, nil, map[string]bool{t.Slug: true}, err
	}
	p, ok := s.reg.Pack(req.Pack, req.Version)
	if !ok {
		return nil, nil, nil, domain.ErrNotFound
	}
	sel, err := s.selection(p, req.Templates)
	if err != nil {
		return nil, nil, nil, err
	}
	c, err := s.reg.Closure(p, sel)
	chosen := map[string]bool{}
	for _, it := range p.Items {
		if (sel == nil && !it.Optional) || sel[it.Template] {
			chosen[it.Template] = true
		}
	}
	return c, p, chosen, err
}

// validateMappings checks, BEFORE any write, that every placeholder is mapped to a resource of THIS tenant.
func (s *TemplateService) validateMappings(ctx context.Context, ts []*templates.Template, given map[string]string) (map[string]string, error) {
	need := templates.Mappings(ts)
	var missing []string
	ids := []uuid.UUID{}
	byID := map[uuid.UUID]string{}
	out := map[string]string{}
	for _, m := range need {
		v, ok := given[m.Key]
		if !ok || strings.TrimSpace(v) == "" {
			missing = append(missing, m.Key)
			continue
		}
		id, err := uuid.Parse(v)
		if err != nil || id == uuid.Nil {
			return nil, fmt.Errorf("%w: mapping %s is not a valid id", domain.ErrInvalid, m.Key)
		}
		out[m.Key] = id.String()
		if m.Kind == "queue" {
			ids = append(ids, id)
			byID[id] = m.Key
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &MissingMappingsError{Keys: missing}
	}
	known := map[string]bool{}
	for _, m := range need {
		known[m.Key] = true
	}
	for k := range given {
		if !known[k] {
			return nil, fmt.Errorf("%w: %q is not a mapping this installation asks for", domain.ErrInvalid, k)
		}
	}
	if len(ids) > 0 && s.resources != nil {
		found, err := s.resources.ExistingQueues(ctx, ids)
		if err != nil {
			return nil, err
		}
		for id, key := range byID {
			if !found[id] {
				return nil, fmt.Errorf("%w: mapping %s points to a queue that does not exist in this tenant", domain.ErrInvalid, key)
			}
		}
	}
	return out, nil
}

// Install creates tenant-owned DRAFTS. All or nothing: any failure undoes every flow created by the call.
func (s *TemplateService) Install(ctx context.Context, req InstallRequest) (*InstallResult, error) {
	tc, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	ts, pack, chosen, err := s.closureFor(req)
	if err != nil {
		return nil, err
	}
	mappings, err := s.validateMappings(ctx, ts, req.Mappings)
	if err != nil {
		return nil, err
	}
	res := &InstallResult{Flows: []InstalledFlow{}}
	err = s.atomic.Do(ctx, func(ctx context.Context) error {
		var packInstall *domain.PackInstallation
		if pack != nil {
			packInstall = &domain.PackInstallation{ID: uuid.New(), TenantID: tc.TenantID, PackSlug: pack.Slug, PackVersion: pack.Version,
				SelectedTemplates: sortedKeys(chosen), Mappings: mappings, InstalledBy: actorPtr(tc), InstalledAt: time.Now().UTC()}
			if err := s.repo.RecordPackInstallation(ctx, packInstall); err != nil {
				return err
			}
			res.PackInstallationID = &packInstall.ID
		}
		finalSlug := map[string]string{} // template slug -> slug it was installed under (collisions get a suffix)
		defaultTaken := map[domain.FlowType]bool{}
		for _, t := range ts {
			slug, err := s.freeSlug(ctx, t.Slug)
			if err != nil {
				return err
			}
			finalSlug[t.Slug] = slug
			resolved, err := templates.Resolve(t.Definition, mappings, finalSlug)
			if err != nil {
				return fmt.Errorf("%w: %v", domain.ErrInvalid, err)
			}
			def, err := domain.ParseDefinition(resolved)
			if err != nil {
				return err
			}
			// Same judgement as a publish, except that a subflow that is still a draft is only a warning.
			issues, _, err := s.cp.check(ctx, slug, def, true)
			if err != nil {
				return err
			}
			if domain.HasErrors(issues) {
				return &PublishError{Issues: issues}
			}
			flow, err := domain.NewFlow(tc.TenantID, slug, t.Name, t.Type, actorPtr(tc))
			if err != nil {
				return err
			}
			flow.Description = t.Description
			flow.DraftDefinition = resolved
			flow.Priority, flow.RestartPolicy = t.Settings.Priority, t.Settings.RestartPolicy
			if t.Settings.IsDefault && !defaultTaken[t.Type] {
				if exists, err := s.repo.ExistsDefault(ctx, t.Type); err != nil {
					return err
				} else if exists {
					res.Notes = append(res.Notes, fmt.Sprintf("%s was not made the default %s flow: the tenant already has one", slug, t.Type))
				} else {
					flow.IsDefault, defaultTaken[t.Type] = true, true
				}
			}
			now := time.Now().UTC()
			flow.SourceTemplateSlug, flow.SourceTemplateVersion, flow.TemplateInstalledAt = &t.Slug, &t.Version, &now
			if err := s.repo.CreateFlow(ctx, flow); err != nil {
				return err
			}
			inst := &domain.TemplateInstallation{ID: uuid.New(), TenantID: tc.TenantID, TemplateSlug: t.Slug, TemplateVersion: t.Version, FlowID: flow.ID, Mappings: onlyUsed(mappings, t),
				InstalledBy: actorPtr(tc), InstalledAt: now}
			if packInstall != nil {
				inst.PackInstallationID = &packInstall.ID
			}
			if err := s.repo.RecordTemplateInstallation(ctx, inst); err != nil {
				return err
			}
			res.Flows = append(res.Flows, InstalledFlow{FlowID: flow.ID, Slug: slug, Template: t.Slug, TemplateVersion: t.Version, IsDefault: flow.IsDefault, Dependency: !chosen[t.Slug], Issues: issues})
			if s.audit != nil {
				s.audit.Record(ctx, AuditTemplateInstalled, flow.ID, map[string]any{"template": t.Slug, "version": t.Version, "slug": slug})
			}
		}
		if packInstall != nil && s.audit != nil && len(res.Flows) > 0 {
			s.audit.Record(ctx, AuditPackInstalled, packInstall.ID, map[string]any{"pack": pack.Slug, "version": pack.Version, "flows": len(res.Flows)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func onlyUsed(mappings map[string]string, t *templates.Template) map[string]string {
	out := map[string]string{}
	for _, m := range t.Mappings {
		out[m.Key] = mappings[m.Key]
	}
	return out
}

func (s *TemplateService) freeSlug(ctx context.Context, base string) (string, error) {
	for i := 1; i <= 20; i++ {
		slug := base
		if i > 1 {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := s.repo.GetFlowBySlug(ctx, slug); errors.Is(err, domain.ErrNotFound) {
			return slug, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: no free slug for %q", domain.ErrInvalid, base)
}
