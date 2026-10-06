package domain

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// ResourceField references a tenant resource (a queue). In a tenant flow it is a UUID string; in a system template it is a
// placeholder {"$ref":"queue.technical"} that the installer resolves. A template never carries a tenant id.
type ResourceField struct {
	ID  string // UUID of a resource of the tenant
	Ref string // template placeholder key
}

func (r ResourceField) IsZero() bool { return r.ID == "" && r.Ref == "" }

func (r ResourceField) UUID() (uuid.UUID, bool) {
	id, err := uuid.Parse(r.ID)
	return id, err == nil && id != uuid.Nil
}

func (r *ResourceField) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*r = ResourceField{}
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*r = ResourceField{ID: s}
		return nil
	}
	var o struct {
		Ref string `json:"$ref"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil || o.Ref == "" {
		return fmt.Errorf("resource must be a uuid string or {\"$ref\": \"kind.name\"}")
	}
	*r = ResourceField{Ref: o.Ref}
	return nil
}

func (r ResourceField) MarshalJSON() ([]byte, error) {
	if r.Ref != "" {
		return json.Marshal(map[string]string{"$ref": r.Ref})
	}
	return json.Marshal(r.ID)
}

// ResourceRef is a reference found in a definition, to be checked against the tenant's own resources.
type ResourceRef struct {
	Kind   string // "queue"
	ID     uuid.UUID
	NodeID string
}
