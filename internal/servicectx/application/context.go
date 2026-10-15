package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// WithTimeline installs the read side of the operational timeline and the customer context.
func (s *Service) WithTimeline(t ports.TimelineRepository) *Service { s.timeline = t; return s }

func (s *Service) pageSize(limit int) int {
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

// SessionTimeline is the history of one session: its stored events plus the routing module's own record of who held the conversation while the session was
// in course (shown by reference, never copied).
func (s *Service) SessionTimeline(ctx context.Context, id uuid.UUID, cursor string, limit int, ascending bool) ([]ports.TimelineItem, string, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, "", err
	}
	if _, err := s.repo.GetSession(ctx, id); err != nil { // visibility first: a session the caller cannot see has no timeline for them
		return nil, "", err
	}
	return s.timeline.Timeline(ctx, ports.TimelineScope{SessionID: &id}, cursor, s.pageSize(limit), ascending)
}

// ContactTimeline is the stored history around one contact, across its sessions.
func (s *Service) ContactTimeline(ctx context.Context, contactID uuid.UUID, cursor string, limit int, ascending bool) ([]ports.TimelineItem, string, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, "", err
	}
	if ok, err := s.repo.ContactExists(ctx, contactID); err != nil {
		return nil, "", err
	} else if !ok {
		return nil, "", domain.ErrNotFound
	}
	return s.timeline.Timeline(ctx, ports.TimelineScope{ContactID: &contactID}, cursor, s.pageSize(limit), ascending)
}

// ContactContext is the aggregated view an attendant needs when a contact writes: recent sessions and results, what is open, and recent events. A contact
// may be tied to several companies (customer accounts): the context never guesses. Without a choice it says NeedsSelection and shows only what is not tied to
// any customer plus the list to choose from; with a choice (which must be one of the contact's own links) it narrows to that customer.
type ContactContext struct {
	ContactID          uuid.UUID
	Customers          []ports.LinkedCustomer
	NeedsSelection     bool
	SelectedCustomerID *uuid.UUID
	ActiveSession      *domain.Session
	RecentSessions     []domain.Session
	RecentEvents       []ports.TimelineItem
	OpenCases          []domain.Case
	PendingTasks       []domain.Task
	ExternalRecords    []domain.ExternalRecord
}

const contextRecentSessions = 5
const contextRecentEvents = 10

func (s *Service) ContactContext(ctx context.Context, contactID uuid.UUID, customerID *uuid.UUID) (*ContactContext, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, err
	}
	if ok, err := s.repo.ContactExists(ctx, contactID); err != nil {
		return nil, err
	} else if !ok {
		return nil, domain.ErrNotFound
	}
	customers, err := s.timeline.LinkedCustomers(ctx, contactID)
	if err != nil {
		return nil, err
	}
	out := &ContactContext{ContactID: contactID, Customers: customers}
	switch {
	case customerID != nil:
		linked := false
		for _, cu := range customers {
			linked = linked || cu.ID == *customerID
		}
		if !linked { // never show a customer's data under a contact that is not tied to it
			return nil, domain.Invalid("customer_id is not linked to this contact")
		}
		out.SelectedCustomerID = customerID
	case len(customers) > 1:
		out.NeedsSelection = true
	case len(customers) == 1:
		id := customers[0].ID
		out.SelectedCustomerID = &id
	}
	filter := out.SelectedCustomerID
	if out.NeedsSelection {
		// no choice yet: only what is tied to no customer, never one company's data picked at random
		filter = nil
	}
	sessions, err := s.timeline.SessionsOfContact(ctx, contactID, filter, contextRecentSessions*3)
	if err != nil {
		return nil, err
	}
	for _, sess := range sessions {
		if out.NeedsSelection && sess.CustomerID != nil {
			continue
		}
		if sess.Status.Active() && out.ActiveSession == nil {
			cp := sess
			out.ActiveSession = &cp
		}
		if len(out.RecentSessions) < contextRecentSessions {
			out.RecentSessions = append(out.RecentSessions, sess)
		}
	}
	if s.ctxRepo != nil {
		if out.OpenCases, err = s.ctxRepo.OpenCasesForContext(ctx, contactID, out.SelectedCustomerID, out.NeedsSelection, defaultContextOpen); err != nil {
			return nil, err
		}
		if out.PendingTasks, err = s.ctxRepo.PendingTasksForContext(ctx, contactID, out.SelectedCustomerID, out.NeedsSelection, defaultContextPending); err != nil {
			return nil, err
		}
		if s.ext != nil {
			if out.ExternalRecords, err = s.ctxRepo.ExternalRecordsForContext(ctx, contactID, out.SelectedCustomerID, out.NeedsSelection, defaultContextOpen); err != nil {
				return nil, err
			}
		}
	}
	events, _, err := s.timeline.Timeline(ctx, ports.TimelineScope{ContactID: &contactID}, "", contextRecentEvents*3, false)
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		if len(out.RecentEvents) == contextRecentEvents {
			break
		}
		if ev.CustomerID != nil && (out.NeedsSelection || (out.SelectedCustomerID != nil && *ev.CustomerID != *out.SelectedCustomerID)) {
			continue // another company's event never rides along
		}
		out.RecentEvents = append(out.RecentEvents, ev)
	}
	return out, nil
}
