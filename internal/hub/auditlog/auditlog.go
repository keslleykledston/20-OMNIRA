// Package auditlog is the Hub administrator's view of "who changed what, where" (ADR-0038 §6, screen "Auditoria"): the configuration changes
// recorded for the instances of THIS hub, in plain facts. Only an active admin of the hub reads it (proven in the reading transaction, like
// the Access panel), and only the changes of the hub's own instances: never another hub's, never the per-message and per-conversation traffic.
//
// What is returned is a WHITELIST of the facts an administrator needs (what happened, to which instance, by whom, through which hub); the raw
// metadata of an audit row is never passed on, so nothing a future event might carry can leak through this screen.
package auditlog

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/authority"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ErrForbidden: not an active admin of this hub (the HTTP layer answers a uniform 404).
var ErrForbidden = errors.New("auditlog: forbidden")

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

type Event struct {
	ID         uuid.UUID      `json:"id"`
	At         time.Time      `json:"at"`
	Action     string         `json:"action"`
	TenantID   *uuid.UUID     `json:"tenant_id,omitempty"`
	TenantName string         `json:"tenant_name,omitempty"`
	ActorID    *uuid.UUID     `json:"actor_id,omitempty"`
	ActorEmail string         `json:"actor_email,omitempty"`
	ActorName  string         `json:"actor_name,omitempty"`
	Via        string         `json:"via,omitempty"` // "hub" when done through the hub's management, "" otherwise
	Facts      map[string]any `json:"facts,omitempty"`
}

type Page struct {
	Items []Event `json:"items"`
	// Next is the cursor for older events; empty when there are no more.
	Next string `json:"next,omitempty"`
}

// factKeys are the only metadata keys passed on (all of them harmless descriptions of a change).
var factKeys = []string{"from", "to", "capability", "provider", "host", "name", "distribution", "members", "instances", "status", "role", "can_manage", "can_reply", "mode", "user_id", "to_user_id", "valid_until"}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// List returns the configuration events of the hub's instances, newest first. tenant (optional) narrows to one instance of THIS hub; before (optional)
// is the cursor of the previous page.
func (s *Service) List(ctx context.Context, actor, hub uuid.UUID, tenant *uuid.UUID, before *time.Time, limit int) (Page, error) {
	if actor == uuid.Nil || hub == uuid.Nil {
		return Page{}, ErrForbidden
	}
	if limit < 1 || limit > MaxLimit {
		limit = DefaultLimit
	}
	out := Page{Items: []Event{}}
	err := platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, s.pool)
		ok, err := authority.LockHubAdmin(c, q, hub, actor)
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
		rows, err := q.Query(c, `
			SELECT e.id, e.created_at, e.action, e.tenant_id, COALESCE(NULLIF(t.trade_name, ''), t.legal_name, ''),
			       e.actor_id, COALESCE(u.email, ''), COALESCE(u.display_name, ''), COALESCE(e.metadata, '{}'::jsonb)
			FROM audit_events e
			LEFT JOIN tenants t ON t.id = e.tenant_id
			LEFT JOIN users u ON u.id = e.actor_id
			WHERE (e.tenant_id IN (SELECT tenant_id FROM hub_tenant_service_contracts WHERE hub_id = $1) OR e.metadata ->> 'hub_id' = $1::text)
			  AND (e.action LIKE 'hub.%' OR e.action LIKE 'platform.%' OR e.action LIKE 'channel.connection_%' OR e.action LIKE 'channel.session_%')
			  AND e.action NOT LIKE 'hub.message.%' AND e.action NOT LIKE 'hub.conversation.%'
			  AND ($2::uuid IS NULL OR e.tenant_id = $2)
			  AND ($3::timestamptz IS NULL OR e.created_at < $3)
			ORDER BY e.created_at DESC, e.id DESC
			LIMIT $4`, hub, tenant, before, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ev Event
			var raw []byte
			if err := rows.Scan(&ev.ID, &ev.At, &ev.Action, &ev.TenantID, &ev.TenantName, &ev.ActorID, &ev.ActorEmail, &ev.ActorName, &raw); err != nil {
				return err
			}
			var meta map[string]any
			if err := json.Unmarshal(raw, &meta); err == nil {
				if v, _ := meta["via"].(string); v == "hub" {
					ev.Via = "hub"
				}
				for _, k := range factKeys {
					if v, ok := meta[k]; ok {
						if ev.Facts == nil {
							ev.Facts = map[string]any{}
						}
						ev.Facts[k] = v
					}
				}
			}
			out.Items = append(out.Items, ev)
		}
		return rows.Err()
	})
	if err != nil {
		return Page{}, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Next = out.Items[limit-1].At.UTC().Format(time.RFC3339Nano)
	}
	return out, nil
}
