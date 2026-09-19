package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusBlocked  Status = "blocked"
	StatusArchived Status = "archived"
)

var e164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// Contact is the provider-neutral customer identity owned by one tenant.
// Provider IDs and channel session details do not belong here.
type Contact struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	DisplayName string
	PhoneE164   string
	Email       string
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func NewContact(tenantID uuid.UUID, phoneE164, displayName string) (*Contact, error) {
	if tenantID == uuid.Nil {
		return nil, errors.New("contact: tenant_id is required")
	}
	phoneE164 = strings.TrimSpace(phoneE164)
	if !e164Pattern.MatchString(phoneE164) {
		return nil, errors.New("contact: phone_e164 must be valid E.164")
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = phoneE164
	}
	now := time.Now().UTC()
	return &Contact{ID: uuid.New(), TenantID: tenantID, DisplayName: name, PhoneE164: phoneE164, Status: StatusActive, CreatedAt: now, UpdatedAt: now}, nil
}

func (c *Contact) SetDisplayName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("contact: display_name is required")
	}
	c.DisplayName = name
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (c *Contact) Block() {
	c.Status = StatusBlocked
	c.UpdatedAt = time.Now().UTC()
}

func (c *Contact) Archive() {
	c.Status = StatusArchived
	c.UpdatedAt = time.Now().UTC()
}
