package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	attendanceapp "github.com/omnira/omnira/internal/attendance/application"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ContactMemory is the intelligence view of the attendance module (ADR-0020). The contact comes from the topic's PRIMARY
// contact (a column the topic already carries, tenant-scoped by RLS and by the explicit filter below), never from a caller.
type ContactMemory struct {
	pool *pgxpool.Pool
	svc  *attendanceapp.Service
}

var _ ports.ContactMemory = (*ContactMemory)(nil)

func NewContactMemory(pool *pgxpool.Pool, svc *attendanceapp.Service) *ContactMemory {
	return &ContactMemory{pool: pool, svc: svc}
}

func (m *ContactMemory) contactOf(ctx context.Context, topicID uuid.UUID) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("intelligence: tenant context required")
	}
	var contact *uuid.UUID
	err = platformdb.QuerierFromContext(ctx, m.pool).QueryRow(ctx,
		`SELECT primary_contact_id FROM topic_threads WHERE tenant_id=$1 AND id=$2`, tc.TenantID, topicID).Scan(&contact)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, domain.ErrTopicNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	if contact == nil {
		return uuid.Nil, domain.ErrReferenceNotFound // a topic without a primary contact (e.g. a group) has no memory
	}
	return *contact, nil
}

func (m *ContactMemory) RecentAttendances(ctx context.Context, topicID uuid.UUID, limit int) ([]ports.MemoryAttendance, error) {
	contact, err := m.contactOf(ctx, topicID)
	if err != nil {
		return nil, err
	}
	h, err := m.svc.HistoryOfContact(ctx, contact, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ports.MemoryAttendance, 0, len(h.Attendances))
	for _, a := range h.Attendances {
		out = append(out, ports.MemoryAttendance{At: a.Closure.CreatedAt, Reason: string(a.Closure.Reason), Summary: a.Closure.Summary,
			Truth: string(a.Closure.SummaryTruth), TicketsKept: a.Closure.TicketsKept})
	}
	return out, nil
}

func (m *ContactMemory) OpenFollowUps(ctx context.Context, topicID uuid.UUID, limit int) ([]ports.MemoryFollowUp, error) {
	contact, err := m.contactOf(ctx, topicID)
	if err != nil {
		return nil, err
	}
	items, err := m.svc.OpenFollowUpsOfContact(ctx, contact, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ports.MemoryFollowUp, 0, len(items))
	for _, f := range items {
		out = append(out, ports.MemoryFollowUp{Kind: string(f.Kind), Text: f.Text, DueAt: f.DueAt, Truth: string(f.Truth), CreatedAt: f.CreatedAt})
	}
	return out, nil
}

func (m *ContactMemory) Search(ctx context.Context, topicID uuid.UUID, query string, limit int) ([]ports.MemoryHit, error) {
	contact, err := m.contactOf(ctx, topicID)
	if err != nil {
		return nil, err
	}
	hits, err := m.svc.SearchHistoryOfContact(ctx, contact, attendancedomain.SearchInput{Query: query, Limit: limit, ExcludeTopicID: &topicID})
	if err != nil {
		return nil, err
	}
	out := make([]ports.MemoryHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, ports.MemoryHit{At: h.At, Role: h.Role, Snippet: h.Snippet})
	}
	return out, nil
}
