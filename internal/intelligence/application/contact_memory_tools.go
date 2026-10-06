package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// Bounds of what a memory tool hands back, so the result always fits the gateway's size limit (12 KiB) and a long summary
// cannot crowd out the rest.
const (
	memoryToolDefaultLimit = 5
	memoryToolTextRunes    = 600
)

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func limitOf(raw json.RawMessage, def int) (int, error) {
	var a struct {
		Limit int `json:"limit"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return 0, domain.ErrInvalidArgs
		}
	}
	if a.Limit <= 0 {
		return def, nil
	}
	return a.Limit, nil
}

func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// NewContactMemoryExecutors binds the three contact.* tools (ADR-0020) to the ContactMemory port. They are read-only, run
// with the requesting user's permissions, and derive the contact from the topic: no argument can name another contact. The
// results carry customer/agent content, which the gateway already flags as untrusted data. A nil memory offers no tool.
func NewContactMemoryExecutors(mem ports.ContactMemory) map[string]ToolExecutor {
	ex := map[string]ToolExecutor{}
	if mem == nil {
		return ex
	}
	ex["contact.recent_attendances"] = func(ctx context.Context, topicID uuid.UUID, args json.RawMessage, _ domain.ToolSource) (any, error) {
		limit, err := limitOf(args, memoryToolDefaultLimit)
		if err != nil {
			return nil, err
		}
		list, err := mem.RecentAttendances(ctx, topicID, limit)
		if err != nil {
			return nil, err
		}
		type item struct {
			Date        string `json:"date"`
			Reason      string `json:"reason"`
			Summary     string `json:"summary"`
			Truth       string `json:"truth"`
			TicketsKept int    `json:"tickets_kept"`
		}
		out := make([]item, 0, len(list))
		for _, a := range list {
			out = append(out, item{day(a.At), a.Reason, clip(a.Summary, memoryToolTextRunes), a.Truth, a.TicketsKept})
		}
		return map[string]any{"attendances": out}, nil
	}
	ex["contact.open_followups"] = func(ctx context.Context, topicID uuid.UUID, args json.RawMessage, _ domain.ToolSource) (any, error) {
		limit, err := limitOf(args, 10)
		if err != nil {
			return nil, err
		}
		list, err := mem.OpenFollowUps(ctx, topicID, limit)
		if err != nil {
			return nil, err
		}
		type item struct {
			Kind  string `json:"kind"`
			Text  string `json:"text"`
			Due   string `json:"due,omitempty"`
			Truth string `json:"truth"`
			Since string `json:"since"`
		}
		out := make([]item, 0, len(list))
		for _, f := range list {
			it := item{Kind: f.Kind, Text: clip(f.Text, memoryToolTextRunes), Truth: f.Truth, Since: day(f.CreatedAt)}
			if f.DueAt != nil {
				it.Due = day(*f.DueAt)
			}
			out = append(out, it)
		}
		return map[string]any{"follow_ups": out}, nil
	}
	ex["contact.search_history"] = func(ctx context.Context, topicID uuid.UUID, args json.RawMessage, _ domain.ToolSource) (any, error) {
		var a struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, domain.ErrInvalidArgs
		}
		if a.Limit <= 0 {
			a.Limit = memoryToolDefaultLimit
		}
		hits, err := mem.Search(ctx, topicID, a.Query, a.Limit)
		if err != nil {
			return nil, err
		}
		type item struct {
			Date    string `json:"date"`
			Role    string `json:"role"`
			Snippet string `json:"snippet"`
		}
		out := make([]item, 0, len(hits))
		for _, h := range hits {
			out = append(out, item{day(h.At), h.Role, h.Snippet})
		}
		return map[string]any{"matches": out}, nil
	}
	return ex
}
