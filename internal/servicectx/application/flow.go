package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
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

// flowCtx admits a call only from a SYSTEM session (a person cannot act as a flow) AND only for a run that exists in this company and attends this very
// conversation: the facade cannot know who is calling, but it refuses to act for a run on a conversation that is not the run's, so a caller inside the system
// cannot sign work as "flow" on an arbitrary conversation.
func (s *Service) flowCtx(ctx context.Context, conversationID, runID uuid.UUID) (context.Context, error) {
	c, err := s.who(ctx)
	if err != nil {
		return ctx, err
	}
	if !c.system {
		return ctx, domain.ErrForbidden
	}
	ok, err := s.repo.FlowRunAttends(ctx, runID, conversationID)
	if err != nil {
		return ctx, err
	}
	if !ok {
		return ctx, domain.ErrForbidden
	}
	return context.WithValue(ctx, flowActorKey{}, true), nil
}

var errNoActiveSession = errors.New("servicectx: the conversation has no active service session")

func flowKey(runID uuid.UUID, seq int) string { return fmt.Sprintf("flow:%s:%d", runID, seq) }

// FlowStart opens the conversation's service session (or returns the one standing).
func (s *Service) FlowStart(ctx context.Context, conversationID, runID uuid.UUID, seq int) (FlowSession, error) {
	ctx, err := s.flowCtx(ctx, conversationID, runID)
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

// FlowClassify puts the session in a category of the company, named by its code. An unknown or inactive code is an error the flow takes on its error port. The step
// leaves a receipt: replayed, it does nothing, even if another session is active by then.
func (s *Service) FlowClassify(ctx context.Context, conversationID, runID uuid.UUID, seq int, categoryCode string) error {
	ctx, err := s.flowCtx(ctx, conversationID, runID)
	if err != nil {
		return err
	}
	hash := fingerprintOf("classify", conversationID.String(), strings.TrimSpace(categoryCode))
	if rec, err := s.replay(ctx, ports.ScopeFlowStep, flowKey(runID, seq), hash); err != nil || rec != nil {
		return err
	}
	if err := s.ClassifyByCode(ctx, conversationID, categoryCode); err != nil {
		return err
	}
	sess, err := s.flowActive(ctx, conversationID)
	if err != nil {
		return err
	}
	return s.remember(ctx, ports.ScopeFlowStep, flowKey(runID, seq), hash, sess.ID)
}

// ClassifyByCode puts the ACTIVE session of the conversation in the category with that code, with the caller's own authority (Patch asks for service.update when
// the caller is a person). Already there = nothing changes, so a replay is harmless.
func (s *Service) ClassifyByCode(ctx context.Context, conversationID uuid.UUID, categoryCode string) error {
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
		return nil
	}
	_, err = s.Patch(ctx, sess.ID, domain.PatchInput{CategoryID: cat}, sess.Version)
	return err
}

// FlowTask makes a pending task for the attendance. Its origin is "flow" and its idempotency is the step itself.
func (s *Service) FlowTask(ctx context.Context, conversationID, runID uuid.UUID, seq int, title, description string, priority domain.Priority) (uuid.UUID, error) {
	ctx, err := s.flowCtx(ctx, conversationID, runID)
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
	ctx, err := s.flowCtx(ctx, conversationID, runID)
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
// active session, or this step already did its work: neither is an error. The step leaves a receipt on the session it closed, so a replay never closes a
// session that was opened afterwards.
func (s *Service) FlowClose(ctx context.Context, conversationID, runID uuid.UUID, seq int, resolutionCode, note string) (bool, error) {
	ctx, err := s.flowCtx(ctx, conversationID, runID)
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
	hash := fingerprintOf("close", conversationID.String(), in.ResolutionCode, in.Note)
	if rec, err := s.replay(ctx, ports.ScopeFlowStep, flowKey(runID, seq), hash); err != nil {
		return false, err
	} else if rec != nil {
		return false, nil
	}
	sess, err := s.flowActive(ctx, conversationID)
	if errors.Is(err, errNoActiveSession) {
		// nothing to close is this step's final outcome: remembered, so a session opened later is not closed by a replay
		return false, s.remember(ctx, ports.ScopeFlowStep, flowKey(runID, seq), hash, uuid.Nil)
	}
	if err != nil {
		return false, err
	}
	if _, err := s.finish(ctx, c, sess, in, domain.SourceFlow, sess.Version); err != nil {
		return false, err
	}
	if err := s.remember(ctx, ports.ScopeFlowStep, flowKey(runID, seq), hash, sess.ID); err != nil {
		return false, err
	}
	return true, nil
}

func fingerprintOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
