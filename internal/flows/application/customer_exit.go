package application

import (
	"context"
	"time"

	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

const exitPendingKey = "_exit_pending"

// maybeCustomerExit implements the contact's "end this attendance" command (see domain.CustomerExit for the guards against
// accidental triggers). It returns handled=true when it consumed the message; false lets the normal flow handle it.
func (e *Engine) maybeCustomerExit(ctx context.Context, facts *ports.ConversationFacts, run *domain.FlowRun, msg *ports.InboundMessage, eventID string) (bool, error) {
	if msg == nil || facts.Kind == "internal" || facts.ContactID == nil {
		return false, nil
	}
	closer, ok := e.effects.(ports.CustomerCloser)
	if !ok {
		return false, nil
	}
	// the entry flow owns the setting (a subflow's own settings never switch it on)
	_, def, err := e.loadDefinition(ctx, run.FlowVersionID)
	if err != nil || def.Settings.CustomerExit == nil || !def.Settings.CustomerExit.Enabled {
		return false, nil
	}
	exit := *def.Settings.CustomerExit

	if pendingAt, pending := e.exitPending(run); pending {
		delete(run.Variables, exitPendingKey)
		expired := e.now().Sub(pendingAt) > time.Duration(domain.ExitPendingTTLSeconds)*time.Second
		switch answer := domain.ExitAnswer(msg.Text); {
		case answer == "yes" && !expired:
			// say goodbye first, then close; a failed goodbye never blocks the closing the person asked for
			_, _ = e.effects.SendText(ctx, facts.ID, exit.FarewellText(), "exit-bye-"+eventID)
			if err := closer.CloseByCustomer(ctx, facts.ID, "confirmed"); err != nil {
				return true, err
			}
			return true, e.endRunByCustomer(ctx, facts, run, eventID)
		case answer == "no" && !expired:
			text := "Certo, vamos continuar."
			if run.Status == domain.RunWaitingHuman {
				text = "Certo! Um atendente vai falar com você em breve."
			}
			_, _ = e.effects.SendText(ctx, facts.ID, text, "exit-keep-"+eventID)
			run.LastEventID = eventID
			return true, e.runs.SaveRun(ctx, run)
		default:
			// Not an answer to the question (or too late): nothing is closed and the message is a normal reply.
			if err := e.runs.SaveRun(ctx, run); err != nil {
				return true, err
			}
			return false, nil
		}
	}

	if !exit.IsCommand(msg.Text) || e.answersWaitingMenu(ctx, run, msg.Text) {
		return false, nil
	}
	if run.Variables == nil {
		run.Variables = map[string]any{}
	}
	run.Variables[exitPendingKey] = e.now().UTC().Format(time.RFC3339)
	run.LastEventID = eventID
	if err := e.runs.SaveRun(ctx, run); err != nil {
		return true, err
	}
	text := domain.ExitConfirmQuestion + "\n1) " + domain.ExitConfirmYesTitle + "\n2) " + domain.ExitConfirmNoTitle
	if cs, ok := e.effects.(ports.ChoiceSender); ok {
		_, err := cs.SendChoice(ctx, facts.ID, text, domain.ExitConfirmQuestion, []ports.ChoiceOption{{ID: "exit_yes", Title: domain.ExitConfirmYesTitle}, {ID: "exit_no", Title: domain.ExitConfirmNoTitle}}, "exit-ask-"+eventID)
		return true, err
	}
	_, err = e.effects.SendText(ctx, facts.ID, text, "exit-ask-"+eventID)
	return true, err
}

func (e *Engine) exitPending(run *domain.FlowRun) (time.Time, bool) {
	raw, ok := run.Variables[exitPendingKey].(string)
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, true // malformed: still pending, and expired by definition
	}
	return at, true
}

// answersWaitingMenu: an option of the choice the bot is waiting on always beats the exit command (a menu may legitimately
// have an option called "Sair").
func (e *Engine) answersWaitingMenu(ctx context.Context, run *domain.FlowRun, text string) bool {
	if run.Status != domain.RunWaitingInput || run.CurrentNodeID == "" {
		return false
	}
	_, def, err := e.loadDefinition(ctx, e.currentVersion(run))
	if err != nil {
		return false
	}
	for _, n := range def.Nodes {
		if n.ID != run.CurrentNodeID || n.Type != domain.NodeChoice {
			continue
		}
		c, err := domain.DecodeConfig[domain.ChoiceConfig](n.Config)
		return err == nil && domain.MatchOption(text, c.Options) >= 0
	}
	return false
}

func (e *Engine) endRunByCustomer(ctx context.Context, facts *ports.ConversationFacts, run *domain.FlowRun, eventID string) error {
	now := e.now()
	run.Status, run.Error, run.WaitUntil, run.CompletedAt, run.UpdatedAt, run.LastEventID = domain.RunCancelled, "the contact ended the attendance", nil, &now, now, eventID
	if err := e.runs.SaveRun(ctx, run); err != nil {
		return err
	}
	e.metrics.Run("customer_exit")
	if facts.AutomationMode != domain.AutomationNone {
		return e.runs.SetAutomationMode(ctx, facts.ID, domain.AutomationNone)
	}
	return nil
}
