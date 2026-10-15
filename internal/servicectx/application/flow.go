package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// The flow engine's door into the service context (ADR-0049 phase 6). A flow acts as the SYSTEM (it is not a person: no key is asked, and every event it writes
// is signed "flow"), inside the step's own transaction, and only for the conversation the run is attending. Each effect is idempotent through the step it
// belongs to: the same (run, step) gives the same session / task / case again, never a second one.

type flowActorKey struct{}

// FlowSession is what a flow learns about the session it works.
type FlowSession struct {
	ID       uuid.UUID
	Protocol string
	Created  bool
}

func (s *Service) flowCtx(ctx context.Context) (context.Context, error) {
	ctx = context.WithValue(ctx, flowActorKey{}, true)
	c, err := s.who(ctx)
	if err != nil {
		return ctx, err
	}
	if !c.system {
		return ctx, domain.ErrForbidden // only the engine's system session is a flow: a person cannot act as one
	}
	return ctx, nil
}

var errNoActiveSession = errors.New("servicectx: the conversation has no active service session")

func flowKey(runID uuid.UUID, seq int) string { return fmt.Sprintf("flow:%s:%d", runID, seq) }

// FlowStart opens the conversation's service session (or returns the one standing).
func (s *Service) FlowStart(ctx context.Context, conversationID, runID uuid.UUID, seq int) (FlowSession, error) {
	ctx, err := s.flowCtx(ctx)
	if err != nil {
		return FlowSession{}, err
	}
	res, err := s.Open(ctx, domain.OpenInput{ConversationID: &conversationID, Source: domain.SourceFlow, IdempotencyKey: flowKey(runID, seq)})
	if err != nil {
		return FlowSession{}, err
	}
	return FlowSession{ID: res.Session.ID, Protocol: res.Session.Protocol, Created: res.Created}, nil
}

func (s *Service) flowActive(ctx context.Context, conversationID uuid.UUID) (*domain.Session, error) {
	if _, err := s.repo.LockConversation(ctx, conversationID); err != nil {
		return nil, err
	}
	sess, err := s.repo.ActiveByConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, errNoActiveSession
	}
	return s.repo.LockSession(ctx, sess.ID)
}

// FlowClassify puts the session in a category of the company, named by its code. An unknown or inactive code is an error the flow takes on its error port.
func (s *Service) FlowClassify(ctx context.Context, conversationID uuid.UUID, categoryCode string) error {
	ctx, err := s.flowCtx(ctx)
	if err != nil {
		return err
	}
	if s.catalog == nil {
		return errors.New("servicectx: catalog not configured")
	}
	code := strings.TrimSpace(categoryCode)
	cats, err := s.catalog.ListCategories(ctx, false)
	if err != nil {
		return err
	}
	var cat *uuid.UUID
	for i := range cats {
		if cats[i].Code == code && cats[i].Active {
			id := cats[i].ID
			cat = &id
		}
	}
	if cat == nil {
		return domain.Invalid("unknown category")
	}
	sess, err := s.flowActive(ctx, conversationID)
	if err != nil {
		return err
	}
	if sess.CategoryID != nil && *sess.CategoryID == *cat {
		return nil // already there: a replayed step changes nothing
	}
	_, err = s.Patch(ctx, sess.ID, domain.PatchInput{CategoryID: cat}, sess.Version)
	return err
}

// FlowTask makes a pending task for the attendance. Its origin is "flow" and its idempotency is the step itself.
func (s *Service) FlowTask(ctx context.Context, conversationID, runID uuid.UUID, seq int, title, description string, priority domain.Priority) (uuid.UUID, error) {
	ctx, err := s.flowCtx(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	facts, err := s.repo.ReadConversation(ctx, conversationID)
	if err != nil {
		return uuid.Nil, err
	}
	in := domain.TaskOpenInput{Title: title, Description: description, Priority: priority, ContactID: facts.ContactID, OriginType: domain.OriginFlow, FlowRunID: &runID, FlowSeq: &seq}
	if sess, err := s.repo.ActiveByConversation(ctx, conversationID); err != nil {
		return uuid.Nil, err
	} else if sess != nil {
		in.SessionID = &sess.ID
	}
	res, err := s.CreateTask(ctx, in)
	if err != nil {
		return uuid.Nil, err
	}
	return res.Task.ID, nil
}

// FlowOpenCase opens a case (ocorrência) from the attendance and returns its number.
func (s *Service) FlowOpenCase(ctx context.Context, conversationID, runID uuid.UUID, seq int, title, description string, priority domain.Priority) (string, error) {
	ctx, err := s.flowCtx(ctx)
	if err != nil {
		return "", err
	}
	in := domain.CaseOpenInput{Title: title, Description: description, Priority: priority, IdempotencyKey: flowKey(runID, seq)}
	if sess, err := s.repo.ActiveByConversation(ctx, conversationID); err != nil {
		return "", err
	} else if sess != nil {
		in.SessionID = &sess.ID
	}
	res, err := s.OpenCase(ctx, in)
	if err != nil {
		return "", err
	}
	return res.Case.CaseNumber, nil
}

// FlowClose ends the conversation's service session with a result. The CONVERSATION is not touched (that is close_conversation). false = there was no
// active session, which is not an error: a replayed step, or an attendance that never had one.
func (s *Service) FlowClose(ctx context.Context, conversationID uuid.UUID, resolutionCode, note string) (bool, error) {
	ctx, err := s.flowCtx(ctx)
	if err != nil {
		return false, err
	}
	c, err := s.who(ctx)
	if err != nil {
		return false, err
	}
	in, err := domain.CloseInput{ResolutionCode: resolutionCode, Note: note}.Normalize()
	if err != nil {
		return false, err
	}
	sess, err := s.flowActive(ctx, conversationID)
	if errors.Is(err, errNoActiveSession) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := s.finish(ctx, c, sess, in, domain.SourceFlow, sess.Version); err != nil {
		return false, err
	}
	return true, nil
}
