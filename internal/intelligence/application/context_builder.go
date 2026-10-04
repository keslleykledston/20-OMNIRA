package application

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ContextBuilder assembles the canonical TopicContext (ADR-0017 Wave 5). It is NOT "the last N messages of the
// conversation": it combines the recent messages OF THE TOPIC, older topic messages that share words with the current
// one, the entities, the confirmed and the machine summaries (kept apart, with their truth level), the tickets and the
// machine reading of attachments. Nothing outside the topic can enter it, and no provider id, phone number or real name does.
type ContextBuilder struct {
	topics    ports.TopicRepository
	data      ports.ContextRepository
	summaries ports.SummaryRepository
	Recent    int
	Relevant  int
	Media     int
}

func NewContextBuilder(topics ports.TopicRepository, data ports.ContextRepository, summaries ports.SummaryRepository) *ContextBuilder {
	return &ContextBuilder{topics: topics, data: data, summaries: summaries, Recent: 20, Relevant: 8, Media: 6}
}

// Build returns the context of a topic. current, when given, must be a message of that same topic.
func (b *ContextBuilder) Build(ctx context.Context, topicID uuid.UUID, current *ports.MessageRef) (domain.TopicContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return domain.TopicContext{}, errors.New("intelligence: tenant context required")
	}
	topic, err := b.topics.GetTopic(ctx, tc.TenantID, topicID)
	if err != nil {
		return domain.TopicContext{}, err
	}
	out := domain.TopicContext{Topic: *topic}

	stats, err := b.data.TopicStats(ctx, tc.TenantID, topicID)
	if err != nil {
		return out, err
	}
	out.MessageCount, out.ParticipantCount, out.LastMessageAt = stats.Messages, stats.Participants, stats.LastAt

	recent, err := b.data.RecentTopicMessages(ctx, tc.TenantID, topicID, b.Recent)
	if err != nil {
		return out, err
	}
	var currentRow *ports.ContextRow
	if current != nil {
		if currentRow, err = b.data.TopicMessage(ctx, tc.TenantID, topicID, *current); err != nil {
			return out, err
		}
	}
	seen := map[uuid.UUID]bool{}
	var excluded []uuid.UUID
	for _, r := range recent {
		seen[r.ID] = true
		excluded = append(excluded, r.ID)
	}
	var relevant []ports.ContextRow
	if currentRow != nil {
		seen[currentRow.ID] = true
		excluded = append(excluded, currentRow.ID)
		if relevant, err = b.data.RelevantTopicMessages(ctx, tc.TenantID, topicID, excluded, domain.Keywords(currentRow.Text, 5), b.Relevant); err != nil {
			return out, err
		}
	}

	aliases := newAliases()
	chrono := func(rows []ports.ContextRow) []domain.ContextMessage {
		sorted := append([]ports.ContextRow(nil), rows...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if !sorted[i].At.Equal(sorted[j].At) {
				return sorted[i].At.Before(sorted[j].At)
			}
			return sorted[i].ID.String() < sorted[j].ID.String()
		})
		msgs := make([]domain.ContextMessage, 0, len(sorted))
		for _, r := range sorted {
			if currentRow != nil && r.ID == currentRow.ID {
				continue // the current message has its own slot
			}
			msgs = append(msgs, toContextMessage(r, aliases))
		}
		return msgs
	}
	// aliases are handed out in chronological order of first appearance across everything included
	out.RelevantMessages = chrono(relevant)
	out.RecentMessages = chrono(recent)
	if currentRow != nil {
		m := toContextMessage(*currentRow, aliases)
		out.CurrentMessage = &m
	}
	out.Truncated = out.MessageCount > len(out.RecentMessages)+len(out.RelevantMessages)+boolInt(out.CurrentMessage != nil)

	if out.Entities, err = b.data.TopicEntities(ctx, tc.TenantID, topicID); err != nil {
		return out, err
	}
	if out.Tickets, err = b.data.TopicTickets(ctx, tc.TenantID, topicID); err != nil {
		return out, err
	}
	if out.Media, err = b.data.TopicMedia(ctx, tc.TenantID, topicID, b.Media); err != nil {
		return out, err
	}
	if confirmed, err := b.summaries.LatestConfirmed(ctx, tc.TenantID, topicID); err != nil {
		return out, err
	} else if confirmed != nil {
		out.ConfirmedSummary = &domain.SummaryContext{Version: confirmed.Version, Status: confirmed.Status, Text: confirmed.SummaryText, Truth: truthOfSummary(confirmed.Status)}
	}
	if inferred, err := b.summaries.LatestInferred(ctx, tc.TenantID, topicID); err != nil {
		return out, err
	} else if inferred != nil {
		out.InferredSummary = &domain.SummaryContext{Version: inferred.Version, Status: inferred.Status, Text: inferred.SummaryText, Truth: domain.TruthAIInferred}
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// truthOfSummary: a person's correction or confirmation outranks a machine's summary.
func truthOfSummary(s domain.SummaryStatus) domain.TruthLevel {
	switch s {
	case domain.SummaryCustomerConfirmed:
		return domain.TruthCustomerConfirmed
	case domain.SummaryAgentConfirmed, domain.SummaryCorrected:
		return domain.TruthAgentConfirmed
	}
	return domain.TruthAIInferred
}

type aliasBook struct {
	byKey map[string]string
	next  int
}

func newAliases() *aliasBook { return &aliasBook{byKey: map[string]string{}} }

// alias hands out "Participante N" per distinct (opaque) participant. Real names and provider ids never reach a model.
func (a *aliasBook) alias(role domain.Role, key string) string {
	switch role {
	case domain.RoleCustomer:
		return "CLIENTE"
	case domain.RoleAgent:
		return "ATENDENTE"
	}
	if key == "" {
		key = "unknown"
	}
	if v, ok := a.byKey[key]; ok {
		return v
	}
	a.next++
	v := fmt.Sprintf("Participante %d", a.next)
	a.byKey[key] = v
	return v
}

func toContextMessage(r ports.ContextRow, a *aliasBook) domain.ContextMessage {
	return domain.ContextMessage{ID: r.ID, Role: r.Role, Alias: a.alias(r.Role, r.ParticipantKey), Text: r.Text, At: r.At, Relation: r.Relation, FromMedia: r.FromMedia}
}
