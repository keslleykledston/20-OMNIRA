import type { OperationalAgent } from './agents'
import type { Queue } from './queues'
import type { TeamMember } from './team'

// Shapes the "Pessoas e grupos" screen works with (camelCase, view-oriented).
export interface PersonGroup {
  memberId: string
  groupId: string
  groupName: string
  available: boolean
  capacity: number
}

export interface PersonRow {
  membershipId: string
  name: string
  email: string
  roleName: string
  operator: null | { profileId: string; status: 'active' | 'disabled'; groups: PersonGroup[] }
}

export interface GroupRow {
  id: string
  name: string
  mode: Queue['mode']
  isDefault: boolean
  memberCount: number
  availableCount: number
  openConversationCount: number
}

const byName = (a: { name: string; email: string }, b: { name: string; email: string }) =>
  (a.name || a.email).localeCompare(b.name || b.email, 'pt-BR', { sensitivity: 'base' })

// A person is a tenant member; they are an "operator" when they have an agent
// profile. Only ACTIVE memberships are shown: a revoked or inactive member cannot
// be made an operator (the API refuses) and would only add noise.
export function buildPeople(members: TeamMember[], agents: OperationalAgent[]): PersonRow[] {
  const profileByMembership = new Map(agents.map((a) => [a.membership_id, a]))
  return members
    .filter((m) => m.status === 'active')
    .map((m): PersonRow => {
      const agent = profileByMembership.get(m.membership_id)
      return {
        membershipId: m.membership_id,
        name: m.name || m.email,
        email: m.email,
        roleName: m.role_name,
        operator: agent
          ? {
              profileId: agent.id,
              status: agent.status,
              groups: agent.queues.map((q) => ({
                memberId: q.id,
                groupId: q.queue_id,
                groupName: q.queue_name,
                available: q.available,
                capacity: q.capacity,
              })),
            }
          : null,
      }
    })
    .sort(byName)
}

export function buildGroups(queues: Queue[]): GroupRow[] {
  return queues.map((q) => ({
    id: q.id,
    name: q.name,
    mode: q.mode,
    isDefault: q.is_default,
    memberCount: q.member_count,
    availableCount: q.available_count,
    openConversationCount: q.open_conversation_count,
  }))
}

// The tenant routes new conversations ONLY to the default group. With none, new
// conversations are not distributed at all, so the screen warns about it.
export function hasNoDefaultGroup(groups: GroupRow[]): boolean {
  return groups.length > 0 && !groups.some((g) => g.isDefault)
}

export function pluralize(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}
