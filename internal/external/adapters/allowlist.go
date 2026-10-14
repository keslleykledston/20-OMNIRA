package adapters

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Allowlists are the queues and lines an installation is limited to. Empty means "all of the tenant".
type Allowlists struct {
	QueueIDs   []uuid.UUID
	ChannelIDs []uuid.UUID
}

// LoadAllowlists reads them through external_installation_allowlists (tenant + installation bound), never from the tables.
func LoadAllowlists(ctx context.Context, q platformdb.Querier, p *domain.Principal) (Allowlists, error) {
	var qs, cs []string
	if err := q.QueryRow(ctx, `SELECT queue_ids, channel_ids FROM external_installation_allowlists($1, $2)`, p.TenantID, p.InstallationID).Scan(&qs, &cs); err != nil {
		return Allowlists{}, err
	}
	return Allowlists{QueueIDs: parseUUIDs(qs), ChannelIDs: parseUUIDs(cs)}, nil
}

func parseUUIDs(in []string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(in))
	for _, s := range in {
		if id, err := uuid.Parse(s); err == nil {
			out = append(out, id)
		}
	}
	return out
}
