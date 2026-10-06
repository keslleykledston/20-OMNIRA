package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// TriggerFilter narrows the channel lines a flow applies to (WAHA and Meta Cloud lines coexist per tenant).
// Empty means every line of the tenant.
type TriggerFilter struct {
	ConnectionIDs []uuid.UUID `json:"connection_ids,omitempty"`
	Providers     []string    `json:"providers,omitempty"`
}

// Matches reports whether a conversation arriving on connection/provider is in scope.
func (f TriggerFilter) Matches(connectionID uuid.UUID, provider string) bool {
	if len(f.ConnectionIDs) > 0 {
		ok := false
		for _, id := range f.ConnectionIDs {
			if id == connectionID {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.Providers) > 0 {
		ok := false
		for _, p := range f.Providers {
			if strings.EqualFold(p, provider) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Flow is the editable logical definition. The draft is never executed; only published versions are.
type Flow struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	Slug                  string
	Name                  string
	Description           string
	Type                  FlowType
	Status                FlowStatus
	DraftDefinition       json.RawMessage
	DraftRevision         int
	ActiveVersionID       *uuid.UUID
	Priority              int
	IsDefault             bool
	TriggerFilter         TriggerFilter
	RestartPolicy         RestartPolicy
	SourceTemplateSlug    *string
	SourceTemplateVersion *int
	TemplateInstalledAt   *time.Time
	CreatedBy             *uuid.UUID
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ArchivedAt            *time.Time
}

// EmptyDefinition is the starting draft of a new flow: nothing but the schema marker.
var EmptyDefinition = json.RawMessage(`{"schema_version":1,"nodes":[],"edges":[],"variables":[],"settings":{},"metadata":{}}`)

func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("%w: slug must be lowercase letters, digits and hyphens (1-64)", ErrInvalid)
	}
	return nil
}

func validateName(name string) error {
	n := utf8.RuneCountInString(strings.TrimSpace(name))
	if n < 1 || n > 120 {
		return fmt.Errorf("%w: name must have 1-120 characters", ErrInvalid)
	}
	return nil
}

// NewFlow starts a draft. It never carries a tenant id from a request: callers pass the TenantContext tenant.
func NewFlow(tenantID uuid.UUID, slug, name string, flowType FlowType, createdBy *uuid.UUID) (*Flow, error) {
	if tenantID == uuid.Nil {
		return nil, fmt.Errorf("%w: tenant is required", ErrInvalid)
	}
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}
	if err := validateName(name); err != nil {
		return nil, err
	}
	if flowType == "" {
		flowType = FlowTypeInbound
	}
	if !flowType.Valid() {
		return nil, fmt.Errorf("%w: unknown flow type %q", ErrInvalid, flowType)
	}
	now := time.Now().UTC()
	return &Flow{
		ID: uuid.New(), TenantID: tenantID, Slug: slug, Name: strings.TrimSpace(name), Type: flowType,
		Status: FlowStatusDraft, DraftDefinition: append(json.RawMessage(nil), EmptyDefinition...), DraftRevision: 1,
		Priority: 100, RestartPolicy: RestartNewConversationOnly, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (f *Flow) HasUnpublishedChanges(publishedRevision int) bool {
	return f.Status == FlowStatusPublished && f.DraftRevision > publishedRevision
}

// FlowVersion is an immutable published snapshot. Rolling back only moves Flow.ActiveVersionID.
type FlowVersion struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	FlowID         uuid.UUID
	Version        int
	Definition     json.RawMessage
	DefinitionHash string
	Note           string
	PublishedBy    *uuid.UUID
	PublishedAt    time.Time
}
