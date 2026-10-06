// Package application holds the flow use cases. The control plane (this file) never executes a flow: it edits drafts,
// validates, publishes immutable versions and switches the active one. Execution lives in the runtime.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Audit action names (strings on purpose: the audit table has no CHECK, and the shared audit domain file stays untouched).
const (
	AuditCreated   = "flow.created"
	AuditSaved     = "flow.draft_saved"
	AuditPublished = "flow.published"
	AuditActivated = "flow.version_activated"
	AuditArchived  = "flow.archived"
	AuditSettings  = "flow.settings_updated"
)

type ControlPlane struct {
	repo      ports.FlowRepository
	resources ports.ResourceChecker
	audit     ports.Auditor
}

func NewControlPlane(repo ports.FlowRepository, resources ports.ResourceChecker, audit ports.Auditor) *ControlPlane {
	return &ControlPlane{repo: repo, resources: resources, audit: audit}
}

// PublishError carries the blocking issues so the UI can show actionable messages.
type PublishError struct{ Issues []domain.Issue }

func (e *PublishError) Error() string {
	n := 0
	for _, i := range e.Issues {
		if i.Severity == domain.SeverityError {
			n++
		}
	}
	return fmt.Sprintf("flows: %d blocking error(s) in the definition", n)
}
func (e *PublishError) Is(target error) bool { return target == domain.ErrNotPublishable }

func actor(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("flows: tenant context required")
	}
	return tc, nil
}

func actorPtr(tc *tenancydomain.TenantContext) *uuid.UUID {
	if tc.ActorID == uuid.Nil {
		return nil
	}
	id := tc.ActorID
	return &id
}

func (c *ControlPlane) record(ctx context.Context, action string, id uuid.UUID, meta map[string]any) {
	if c.audit != nil {
		c.audit.Record(ctx, action, id, meta)
	}
}

type CreateInput struct {
	Slug, Name, Description string
	Type                    domain.FlowType
}

func (c *ControlPlane) Create(ctx context.Context, in CreateInput) (*domain.Flow, error) {
	tc, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	f, err := domain.NewFlow(tc.TenantID, in.Slug, in.Name, in.Type, actorPtr(tc))
	if err != nil {
		return nil, err
	}
	if len(in.Description) > 2000 {
		return nil, fmt.Errorf("%w: description is limited to 2000 characters", domain.ErrInvalid)
	}
	f.Description = in.Description
	if err := c.repo.CreateFlow(ctx, f); err != nil {
		return nil, err
	}
	c.record(ctx, AuditCreated, f.ID, map[string]any{"slug": f.Slug, "type": string(f.Type)})
	return f, nil
}

func (c *ControlPlane) Get(ctx context.Context, id uuid.UUID) (*domain.Flow, error) {
	return c.repo.GetFlow(ctx, id)
}

func (c *ControlPlane) List(ctx context.Context, f ports.ListFilter) ([]*domain.Flow, error) {
	return c.repo.ListFlows(ctx, f)
}

func (c *ControlPlane) Versions(ctx context.Context, flowID uuid.UUID, limit, offset int) ([]*domain.FlowVersion, error) {
	if _, err := c.repo.GetFlow(ctx, flowID); err != nil {
		return nil, err
	}
	return c.repo.ListVersions(ctx, flowID, limit, offset)
}

func (c *ControlPlane) Version(ctx context.Context, flowID uuid.UUID, version int) (*domain.FlowVersion, error) {
	return c.repo.GetVersionByNumber(ctx, flowID, version)
}

// CheckDefinition validates a parsed definition: structure (pure) plus the tenant resources it references.
// A queue or line that does not exist in THIS tenant (including one that belongs to another tenant) is an error.
func (c *ControlPlane) CheckDefinition(ctx context.Context, def *domain.Definition, allowPlaceholders bool) ([]domain.Issue, error) {
	issues, _, err := c.check(ctx, "", def, allowPlaceholders)
	return issues, err
}

// check is the full judgement used by editing, validation and publishing: pure rules, tenant resources and subflows.
// slug is the flow's own slug ("" for an unsaved definition) to catch self-calls. For a template (allowPlaceholders) a
// missing subflow is only a warning: the pack's subflows are still drafts at install time.
func (c *ControlPlane) check(ctx context.Context, slug string, def *domain.Definition, allowPlaceholders bool) ([]domain.Issue, map[string]uuid.UUID, error) {
	issues := domain.Validate(def, domain.ValidateOptions{AllowPlaceholders: allowPlaceholders})
	if refs := domain.ExtractRefs(def); len(refs) > 0 && c.resources != nil {
		ids := make([]uuid.UUID, 0, len(refs))
		for _, r := range refs {
			ids = append(ids, r.ID)
		}
		found, err := c.resources.ExistingQueues(ctx, ids)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range refs {
			if !found[r.ID] {
				issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "resource_not_found", NodeID: r.NodeID,
					Message: fmt.Sprintf("%q references a %s that does not exist in this tenant", r.NodeID, r.Kind)})
			}
		}
	}
	pins, pinIssues, err := c.pinSubflows(ctx, slug, def)
	if err != nil {
		return nil, nil, err
	}
	for _, i := range pinIssues {
		if allowPlaceholders && i.Code == "subflow_not_found" {
			i.Severity = domain.SeverityWarning
		}
		issues = append(issues, i)
	}
	return issues, pins, nil
}

// ValidateRaw validates an unsaved definition (live validation while editing). Structural parse failures are reported as
// a single error issue instead of an exception.
func (c *ControlPlane) ValidateRaw(ctx context.Context, raw []byte) ([]domain.Issue, error) {
	def, err := domain.ParseDefinition(raw)
	if err != nil {
		return []domain.Issue{{Severity: domain.SeverityError, Code: "invalid_definition", Message: err.Error()}}, nil
	}
	return c.CheckDefinition(ctx, def, false)
}

type DraftResult struct {
	Flow   *domain.Flow
	Issues []domain.Issue
}

// SaveDraft stores work in progress. It must parse (known fields, size limits) but may still have semantic errors:
// blocking those is Publish's job.
func (c *ControlPlane) SaveDraft(ctx context.Context, id uuid.UUID, expectedRevision int, name, description string, raw json.RawMessage) (*DraftResult, error) {
	def, err := domain.ParseDefinition(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" || len([]rune(name)) > 120 || len(description) > 2000 {
		return nil, fmt.Errorf("%w: name (1-120) or description (max 2000) out of range", domain.ErrInvalid)
	}
	f, err := c.repo.SaveDraft(ctx, id, expectedRevision, strings.TrimSpace(name), description, raw)
	if err != nil {
		return nil, err
	}
	issues, _, err := c.check(ctx, f.Slug, def, false)
	if err != nil {
		return nil, err
	}
	c.record(ctx, AuditSaved, f.ID, map[string]any{"revision": f.DraftRevision})
	return &DraftResult{Flow: f, Issues: issues}, nil
}

// ValidateStored validates the stored draft of a flow.
func (c *ControlPlane) ValidateStored(ctx context.Context, id uuid.UUID) (*DraftResult, error) {
	f, err := c.repo.GetFlow(ctx, id)
	if err != nil {
		return nil, err
	}
	def, err := domain.ParseDefinition(f.DraftDefinition)
	if err != nil {
		return &DraftResult{Flow: f, Issues: []domain.Issue{{Severity: domain.SeverityError, Code: "invalid_definition", Message: err.Error()}}}, nil
	}
	issues, _, err := c.check(ctx, f.Slug, def, false)
	if err != nil {
		return nil, err
	}
	return &DraftResult{Flow: f, Issues: issues}, nil
}

var settingsProviderPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// UpdateSettings edits the resolver-facing attributes. Channel lines in the filter must belong to the tenant.
func (c *ControlPlane) UpdateSettings(ctx context.Context, id uuid.UUID, s ports.Settings) (*domain.Flow, error) {
	if s.Priority < 1 || s.Priority > 1000 {
		return nil, fmt.Errorf("%w: priority must be between 1 and 1000", domain.ErrInvalid)
	}
	if s.RestartPolicy != domain.RestartNewConversationOnly && s.RestartPolicy != domain.RestartAlways {
		return nil, fmt.Errorf("%w: unknown restart policy %q", domain.ErrInvalid, s.RestartPolicy)
	}
	if len(s.TriggerFilter.ConnectionIDs) > 50 || len(s.TriggerFilter.Providers) > 10 {
		return nil, fmt.Errorf("%w: trigger filter is too large", domain.ErrInvalid)
	}
	for _, p := range s.TriggerFilter.Providers {
		if !settingsProviderPattern.MatchString(p) {
			return nil, fmt.Errorf("%w: provider %q is invalid", domain.ErrInvalid, p)
		}
	}
	if len(s.TriggerFilter.ConnectionIDs) > 0 && c.resources != nil {
		found, err := c.resources.ExistingConnections(ctx, s.TriggerFilter.ConnectionIDs)
		if err != nil {
			return nil, err
		}
		for _, cid := range s.TriggerFilter.ConnectionIDs {
			if !found[cid] {
				return nil, fmt.Errorf("%w: channel line %s does not exist in this tenant", domain.ErrInvalid, cid)
			}
		}
	}
	f, err := c.repo.UpdateSettings(ctx, id, s)
	if err != nil {
		return nil, err
	}
	c.record(ctx, AuditSettings, f.ID, map[string]any{"priority": s.Priority, "default": s.IsDefault})
	return f, nil
}

type PublishResult struct {
	Version  *domain.FlowVersion
	Warnings []domain.Issue
}

// Publish validates the draft the caller saw (expectedRevision), pins its subflows and snapshots it into a new immutable
// version that becomes the active one. Blocking errors return a *PublishError and nothing is written.
func (c *ControlPlane) Publish(ctx context.Context, id uuid.UUID, expectedRevision int, note string) (*PublishResult, error) {
	tc, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if len(note) > 500 {
		return nil, fmt.Errorf("%w: note is limited to 500 characters", domain.ErrInvalid)
	}
	f, err := c.repo.GetFlow(ctx, id)
	if err != nil {
		return nil, err
	}
	if f.Status == domain.FlowStatusArchived {
		return nil, domain.ErrArchived
	}
	if f.DraftRevision != expectedRevision {
		return nil, domain.ErrRevisionConflict
	}
	def, err := domain.ParseDefinition(f.DraftDefinition)
	if err != nil {
		return nil, &PublishError{Issues: []domain.Issue{{Severity: domain.SeverityError, Code: "invalid_definition", Message: err.Error()}}}
	}
	issues, pins, err := c.check(ctx, f.Slug, def, false)
	if err != nil {
		return nil, err
	}
	if domain.HasErrors(issues) {
		return nil, &PublishError{Issues: issues}
	}
	v, err := c.repo.Publish(ctx, id, expectedRevision, note, actorPtr(tc), pins)
	if err != nil {
		return nil, err
	}
	var warnings []domain.Issue
	for _, i := range issues {
		if i.Severity == domain.SeverityWarning {
			warnings = append(warnings, i)
		}
	}
	c.record(ctx, AuditPublished, id, map[string]any{"version": v.Version, "hash": v.DefinitionHash, "warnings": len(warnings)})
	return &PublishResult{Version: v, Warnings: warnings}, nil
}

// pinSubflows resolves every subflow node to the version that is active NOW and rejects self-reference, missing
// subflows and a call depth beyond the limit. Versions only reference older versions, so recursion cannot form.
func (c *ControlPlane) pinSubflows(ctx context.Context, slug string, def *domain.Definition) (map[string]uuid.UUID, []domain.Issue, error) {
	pins := map[string]uuid.UUID{}
	var issues []domain.Issue
	for _, n := range def.Nodes {
		if n.Type != domain.NodeSubflow {
			continue
		}
		cfg, err := domain.DecodeConfig[domain.SubflowConfig](n.Config)
		if err != nil || cfg.Flow == "" {
			continue // already reported by the validator
		}
		if slug != "" && cfg.Flow == slug {
			issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "subflow_self", NodeID: n.ID, Message: fmt.Sprintf("%q calls the flow it belongs to", n.ID)})
			continue
		}
		_, ver, err := c.repo.ActiveSubflow(ctx, cfg.Flow)
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrNoSuchVersion) {
			issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "subflow_not_found", NodeID: n.ID, Message: fmt.Sprintf("%q calls %q, which is not a published SUBFLOW flow of this tenant", n.ID, cfg.Flow)})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		pins[cfg.Flow] = ver.ID
		depth, err := c.depth(ctx, ver, 1)
		if err != nil {
			return nil, nil, err
		}
		if depth > domain.MaxSubflowDepth {
			issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "subflow_too_deep", NodeID: n.ID, Message: fmt.Sprintf("%q would nest subflows %d levels deep (max %d)", n.ID, depth, domain.MaxSubflowDepth)})
		}
	}
	return pins, issues, nil
}

func (c *ControlPlane) depth(ctx context.Context, v *domain.FlowVersion, level int) (int, error) {
	max := level
	if level > domain.MaxSubflowDepth+1 {
		return level, nil
	}
	for _, id := range v.SubflowPins {
		child, err := c.repo.GetVersion(ctx, id)
		if err != nil {
			return 0, err
		}
		d, err := c.depth(ctx, child, level+1)
		if err != nil {
			return 0, err
		}
		if d > max {
			max = d
		}
	}
	return max, nil
}

// Activate points NEW runs at an existing version (rollback). Runs already started stay on their own version.
func (c *ControlPlane) Activate(ctx context.Context, flowID uuid.UUID, version int) (*domain.Flow, *domain.FlowVersion, error) {
	f, v, err := c.repo.ActivateVersion(ctx, flowID, version)
	if err != nil {
		return nil, nil, err
	}
	c.record(ctx, AuditActivated, flowID, map[string]any{"version": v.Version})
	return f, v, nil
}

func (c *ControlPlane) Archive(ctx context.Context, id uuid.UUID) error {
	if err := c.repo.Archive(ctx, id); err != nil {
		return err
	}
	c.record(ctx, AuditArchived, id, nil)
	return nil
}

// ActiveVersion returns the version new runs of f use, or nil when it was never published.
func (c *ControlPlane) ActiveVersion(ctx context.Context, f *domain.Flow) (*domain.FlowVersion, error) {
	if f.ActiveVersionID == nil {
		return nil, nil
	}
	return c.repo.GetVersion(ctx, *f.ActiveVersionID)
}

// NodeCatalog is the node library for the builder UI, derived from the same specs the validator uses.
type NodeCatalogEntry struct {
	Type       domain.NodeType   `json:"type"`
	Label      string            `json:"label"`
	Category   string            `json:"category"`
	SideEffect domain.SideEffect `json:"side_effect"`
	Waits      bool              `json:"waits"`
	Terminal   bool              `json:"terminal"`
	Ports      []string          `json:"ports"` // static ports of an unconfigured node; choice/switch add one per option/case
}

func NodeCatalog() []NodeCatalogEntry {
	specs := domain.Specs()
	out := make([]NodeCatalogEntry, 0, len(specs))
	for _, s := range specs {
		ports := []string{}
		for _, p := range s.Analyze(domain.Node{ID: "x", Type: s.Type}).Ports {
			ports = append(ports, p.Name)
		}
		out = append(out, NodeCatalogEntry{Type: s.Type, Label: s.Label, Category: s.Category, SideEffect: s.SideEffect, Waits: s.Waits, Terminal: s.Terminal, Ports: ports})
	}
	return out
}
