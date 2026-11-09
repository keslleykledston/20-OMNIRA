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

// WahaDirectory lists the groups of one of the tenant's active WAHA connections. A tenant may run more
// than one WhatsApp instance at once; connectionHint uuid.Nil picks the tenant's single active connection
// (ambiguous and refused, rather than guessed, when there is more than one); a non-nil hint picks exactly
// that connection, refused if it is not an active WAHA connection of THIS tenant.
type WahaDirectory struct {
	conns  ConnectionFinder
	lister GroupLister
}

func NewWahaDirectory(conns ConnectionFinder, lister GroupLister) *WahaDirectory {
	return &WahaDirectory{conns: conns, lister: lister}
}

func (d *WahaDirectory) List(ctx context.Context, tenantID, connectionHint uuid.UUID) (uuid.UUID, []channeldomain.ProviderGroup, error) {
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
	var chosen *channeldomain.ChannelConnection
	if connectionHint != uuid.Nil {
		for _, c := range active {
			if c.ID == connectionHint {
				chosen = c
				break
			}
		}
		if chosen == nil {
			return uuid.Nil, nil, ErrNoConnection
		}
	} else {
		switch len(active) {
		case 0:
			return uuid.Nil, nil, ErrNoConnection
		case 1:
			chosen = active[0]
		default:
			return uuid.Nil, nil, ErrAmbiguousConnection
		}
	}
	groups, err := d.lister.ListGroups(ctx, *chosen)
	if err != nil {
		return uuid.Nil, nil, err
	}
	return chosen.ID, groups, nil
}
