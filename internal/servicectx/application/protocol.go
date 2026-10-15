package application

import (
	"context"

	"github.com/omnira/omnira/internal/servicectx/domain"
)

const (
	ActionPrefixSet = "service_settings.protocol_prefix_set"
)

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
	p, err := c.svc.repo.SuggestPrefix(ctx)
	if err != nil {
		return ProtocolSettings{}, err
	}
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
