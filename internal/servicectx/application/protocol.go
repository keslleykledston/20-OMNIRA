package application

import (
	"context"
	"errors"

	"github.com/omnira/omnira/internal/servicectx/domain"
)

const (
	ActionPrefixSet = "service_settings.protocol_prefix_set"
	// maxPrefixTries bounds how many candidates one first protocol tries before it asks the company to choose a prefix itself.
	maxPrefixTries = 80
)

// nextSessionProtocol takes the next protocol of the company. A company that has a counter row uses its prefix (chosen by its administrator, or derived at the first
// protocol); the first protocol of a company that chose nothing derives the prefix from its name and takes the first one that no other company uses. Every attempt
// runs under a savepoint, so a taken prefix leaves nothing behind.
func (s *Service) nextSessionProtocol(ctx context.Context) (string, int64, error) {
	cur, _, found, err := s.repo.SessionPrefix(ctx)
	if err != nil {
		return "", 0, err
	}
	if found {
		return s.repo.NextProtocol(ctx, "session", cur, domain.DefaultSessionStart)
	}
	name, err := s.repo.TenantName(ctx)
	if err != nil {
		return "", 0, err
	}
	for i, cand := range domain.PrefixCandidates(name) {
		if i >= maxPrefixTries {
			break
		}
		var prefix string
		var seq int64
		err := s.repo.InSavepoint(ctx, func(ctx context.Context) (e error) {
			prefix, seq, e = s.repo.NextProtocol(ctx, "session", cand, domain.DefaultSessionStart)
			return e
		})
		if errors.Is(err, domain.ErrPrefixTaken) {
			continue
		}
		return prefix, seq, err
	}
	return "", 0, domain.ErrPrefixTaken
}

// ProtocolSettings is what the administrator sees about the company's protocol: the prefix, whether it is final, and the next number.
type ProtocolSettings struct {
	Prefix string
	// Configured: the company has a counter row (it chose a prefix or already issued protocols). Otherwise Prefix is only the suggestion, not yet reserved.
	Configured bool
	// Locked: a protocol was issued, so the prefix can no longer change.
	Locked bool
	Next   string
}

func (c *Catalog) ProtocolSettings(ctx context.Context) (ProtocolSettings, error) {
	if err := c.check(ctx, PermRead); err != nil {
		return ProtocolSettings{}, err
	}
	return c.settings(ctx)
}

func (c *Catalog) settings(ctx context.Context) (ProtocolSettings, error) {
	prefix, last, found, err := c.svc.repo.SessionPrefix(ctx)
	if err != nil {
		return ProtocolSettings{}, err
	}
	if found {
		next := domain.DefaultSessionStart
		if last > 0 {
			next = last + 1
		}
		return ProtocolSettings{Prefix: prefix, Configured: true, Locked: last > 0, Next: domain.FormatProtocol(prefix, next)}, nil
	}
	name, err := c.svc.repo.TenantName(ctx)
	if err != nil {
		return ProtocolSettings{}, err
	}
	p := domain.PrefixCandidates(name)[0]
	return ProtocolSettings{Prefix: p, Next: domain.FormatProtocol(p, domain.DefaultSessionStart)}, nil
}

// SetProtocolPrefix reserves the company's prefix (two to six capital letters). It works only before the first protocol; afterwards the same prefix is accepted
// as a repeat and any other is refused (ErrPrefixLocked). A prefix another company uses is refused (ErrPrefixTaken).
func (c *Catalog) SetProtocolPrefix(ctx context.Context, raw string) (ProtocolSettings, error) {
	if err := c.check(ctx, PermManageCatalog); err != nil {
		return ProtocolSettings{}, err
	}
	prefix := domain.NormalizePrefix(raw)
	if !domain.ValidPrefix(prefix) {
		return ProtocolSettings{}, domain.Invalid("prefix must be two to six capital letters, without digits (K3G becomes KTG)")
	}
	w, err := c.svc.who(ctx)
	if err != nil {
		return ProtocolSettings{}, err
	}
	if err := c.repo.ReserveSessionPrefix(ctx, prefix); err != nil {
		return ProtocolSettings{}, err
	}
	c.svc.record(ctx, w, ActionPrefixSet, "service_settings", w.tc.TenantID, map[string]any{"prefix": prefix})
	return c.settings(ctx)
}
