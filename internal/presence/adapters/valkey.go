package adapters

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/omnira/omnira/internal/presence/ports"
)

// keyExpiryZSet is a single global sorted set (member -> expiry unix score)
// covering every tenant. ZRANGEBYSCORE with a bounded LIMIT is an index
// lookup, not a keyspace scan: cost is proportional to entries actually due,
// never to the number of tenants or agents. This is what ADR-0010 §17 (scale)
// and the reaper requirement ("evitar SCAN global") require.
const keyExpiryZSet = "presence:expiry"

func sessionsKey(tenantID, agentProfileID uuid.UUID) string {
	return fmt.Sprintf("presence:sessions:%s:%s", tenantID, agentProfileID)
}

func onlineKey(tenantID uuid.UUID) string {
	return fmt.Sprintf("presence:online:%s", tenantID)
}

// expiryMember embeds identity in the zset member using "|", which never
// appears in a UUID, so parsing is unambiguous.
func expiryMember(tenantID, agentProfileID uuid.UUID, sessionID string) string {
	return tenantID.String() + "|" + agentProfileID.String() + "|" + sessionID
}

// touchScript is atomic: SADD's return value (1 = new member) combined with
// the post-add SCARD tells us, in one round trip and one Redis event-loop
// tick, whether this is the agent's first live session (online transition) or
// a refresh/new-tab-while-already-online (no transition) — exactly the
// semantics ADR-0010 §6 and the IAM4.2-A test matrix require. Redis/Valkey
// executes scripts single-threaded, so this is race-free even with many API
// processes heartbeating concurrently.
var touchScript = redis.NewScript(`
local added = redis.call('SADD', KEYS[1], ARGV[1])
redis.call('ZADD', KEYS[2], ARGV[2], ARGV[3])
local card = redis.call('SCARD', KEYS[1])
if added == 1 and card == 1 then
  redis.call('SADD', KEYS[3], ARGV[4])
  return 1
end
return 0
`)

// expireMemberScript retires exactly one expired session. ZREM is the
// linearization point: if two reaper processes race on the same expired
// member (possible with more than one worker instance), only the one whose
// ZREM actually removes it proceeds to SREM/SCARD/possible offline
// transition; the other observes removed==0 and does nothing. Because
// Redis/Valkey serializes command execution, there is no window where both
// could observe count==0 for the same agent.
var expireMemberScript = redis.NewScript(`
local removed = redis.call('ZREM', KEYS[1], ARGV[1])
if removed == 0 then
  return -1
end
redis.call('SREM', KEYS[2], ARGV[2])
local card = redis.call('SCARD', KEYS[2])
if card == 0 then
  redis.call('SREM', KEYS[3], ARGV[3])
  return 0
end
return card
`)

// Store is the Valkey-backed realtime presence store (ADR-0010).
type Store struct {
	rdb redis.Scripter
	cli *redis.Client // nil in tests using a lighter Scripter; used only for ZRangeByScore/SMembers
}

var _ ports.Store = (*Store)(nil)

func NewStore(cli *redis.Client) *Store {
	return &Store{rdb: cli, cli: cli}
}

func (s *Store) Touch(ctx context.Context, tenantID, agentProfileID uuid.UUID, sessionID string, ttl time.Duration) (bool, error) {
	if sessionID == "" {
		return false, fmt.Errorf("presence: session id is required")
	}
	sk := sessionsKey(tenantID, agentProfileID)
	member := expiryMember(tenantID, agentProfileID, sessionID)
	score := float64(time.Now().Add(ttl).Unix())
	res, err := touchScript.Run(ctx, s.rdb,
		[]string{sk, keyExpiryZSet, onlineKey(tenantID)},
		sessionID, score, member, agentProfileID.String(),
	).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (s *Store) ExpireBatch(ctx context.Context, now time.Time, limit int) ([]ports.AgentRef, int, error) {
	if limit <= 0 {
		limit = 500
	}
	if s.cli == nil {
		return nil, 0, fmt.Errorf("presence: store not configured for expiry scan")
	}
	members, err := s.cli.ZRangeByScore(ctx, keyExpiryZSet, &redis.ZRangeBy{
		Min:    "-inf",
		Max:    strconv.FormatInt(now.Unix(), 10),
		Offset: 0,
		Count:  int64(limit),
	}).Result()
	if err != nil {
		return nil, 0, err
	}
	offline := make([]ports.AgentRef, 0)
	for _, member := range members {
		parts := strings.SplitN(member, "|", 3)
		if len(parts) != 3 {
			// Malformed entry from an unexpected write path; drop it rather
			// than looping on it forever.
			s.cli.ZRem(ctx, keyExpiryZSet, member)
			continue
		}
		tenantID, err1 := uuid.Parse(parts[0])
		agentID, err2 := uuid.Parse(parts[1])
		sessionID := parts[2]
		if err1 != nil || err2 != nil || sessionID == "" {
			s.cli.ZRem(ctx, keyExpiryZSet, member)
			continue
		}
		res, err := expireMemberScript.Run(ctx, s.rdb,
			[]string{keyExpiryZSet, sessionsKey(tenantID, agentID), onlineKey(tenantID)},
			member, sessionID, agentID.String(),
		).Int()
		if err != nil {
			return offline, len(members), err
		}
		if res == -1 {
			// Already retired by another reaper instance; not our transition to report.
			continue
		}
		if res == 0 {
			offline = append(offline, ports.AgentRef{TenantID: tenantID, AgentProfileID: agentID})
		}
	}
	return offline, len(members), nil
}

func (s *Store) Snapshot(ctx context.Context, tenantID uuid.UUID) ([]uuid.UUID, error) {
	if s.cli == nil {
		return nil, fmt.Errorf("presence: store not configured for snapshot")
	}
	members, err := s.cli.SMembers(ctx, onlineKey(tenantID)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(members))
	for _, m := range members {
		id, err := uuid.Parse(m)
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

// OnlineMembers checks a bounded batch of agent_profile_ids against the
// tenant's online set in one round trip (SMISMEMBER) — used by IAM4.2-B1
// routing presence enforcement to avoid a Valkey call per candidate. Never
// call this once per candidate; the caller chunks its candidate list and
// calls this once per chunk.
func (s *Store) OnlineMembers(ctx context.Context, tenantID uuid.UUID, agentProfileIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if len(agentProfileIDs) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	if s.cli == nil {
		return nil, fmt.Errorf("presence: store not configured for online lookup")
	}
	args := make([]interface{}, len(agentProfileIDs))
	for i, id := range agentProfileIDs {
		args[i] = id.String()
	}
	res, err := s.cli.SMIsMember(ctx, onlineKey(tenantID), args...).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(agentProfileIDs))
	for i, online := range res {
		out[agentProfileIDs[i]] = online
	}
	return out, nil
}
