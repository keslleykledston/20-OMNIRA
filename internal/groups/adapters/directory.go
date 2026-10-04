package adapters

import (
	"context"

	"github.com/google/uuid"

	channeldomain "github.com/omnira/omnira/internal/channels/domain"
)

// ConnectionFinder is the part of the channel connection repository the directory needs.
type ConnectionFinder interface {
	FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*channeldomain.ChannelConnection, error)
}

// GroupLister is the provider call that lists the groups of a connection's WhatsApp account.
type GroupLister interface {
	ListGroups(ctx context.Context, conn channeldomain.ChannelConnection) ([]channeldomain.ProviderGroup, error)
}

// WahaDirectory lists the groups of the tenant's single active WAHA connection. More than one
// active connection is ambiguous and refused rather than guessed.
type WahaDirectory struct {
	conns  ConnectionFinder
	lister GroupLister
}

func NewWahaDirectory(conns ConnectionFinder, lister GroupLister) *WahaDirectory {
	return &WahaDirectory{conns: conns, lister: lister}
}

func (d *WahaDirectory) List(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, []channeldomain.ProviderGroup, error) {
	all, err := d.conns.FindByTenant(ctx, tenantID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	var active []*channeldomain.ChannelConnection
	for _, c := range all {
		if c != nil && c.TenantID == tenantID && c.Provider == channeldomain.ProviderWAHA && c.Status == channeldomain.ConnectionStatusActive {
			active = append(active, c)
		}
	}
	switch len(active) {
	case 0:
		return uuid.Nil, nil, ErrNoConnection
	case 1:
	default:
		return uuid.Nil, nil, ErrAmbiguousConnection
	}
	groups, err := d.lister.ListGroups(ctx, *active[0])
	if err != nil {
		return uuid.Nil, nil, err
	}
	return active[0].ID, groups, nil
}
