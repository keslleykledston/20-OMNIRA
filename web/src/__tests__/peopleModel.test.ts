import { describe, expect, it } from 'vitest'
import { buildGroups, buildPeople, hasNoDefaultGroup, pluralize } from '../lib/peopleModel'
import type { OperationalAgent } from '../lib/agents'
import type { Queue } from '../lib/queues'
import type { TeamMember } from '../lib/team'

const member = (over: Partial<TeamMember> = {}): TeamMember => ({
  membership_id: 'm1', user_id: 'u1', name: 'Ana Souza', email: 'ana@k3g.com', role_key: 'tenant_agent',
  role_name: 'Agente', status: 'active', created_at: '2026-01-01T00:00:00Z', ...over,
})
const agent = (over: Partial<OperationalAgent> = {}): OperationalAgent => ({
  id: 'p1', membership_id: 'm1', user_id: 'u1', name: 'Ana Souza', email: 'ana@k3g.com', role: 'Agente', status: 'active',
  queues: [{ id: 'qm1', queue_id: 'q1', queue_name: 'Suporte', available: true, capacity: 3 }], ...over,
})
const queue = (over: Partial<Queue> = {}): Queue => ({
  id: 'q1', name: 'Suporte', mode: 'round_robin', is_default: true, member_count: 2, available_count: 1,
  open_conversation_count: 5, created_at: '', updated_at: '', ...over,
})

describe('buildPeople', () => {
  it('marks a member with an agent profile as operator, with the groups they attend', () => {
    const [p] = buildPeople([member()], [agent()])
    expect(p.operator).toEqual({
      profileId: 'p1', status: 'active',
      groups: [{ memberId: 'qm1', groupId: 'q1', groupName: 'Suporte', available: true, capacity: 3 }],
    })
  })

  it('leaves a member without a profile as a non-operator', () => {
    expect(buildPeople([member()], [])[0].operator).toBeNull()
  })

  it('keeps a paused operator as paused, not as a non-operator', () => {
    expect(buildPeople([member()], [agent({ status: 'disabled' })])[0].operator?.status).toBe('disabled')
  })

  // The API refuses to make an inactive/revoked member an operator, so listing them only adds noise.
  it('shows only active memberships', () => {
    const people = buildPeople(
      [member(), member({ membership_id: 'm2', name: 'Rev', status: 'revoked' }), member({ membership_id: 'm3', name: 'Ina', status: 'inactive' })],
      [],
    )
    expect(people.map((p) => p.membershipId)).toEqual(['m1'])
  })

  it('sorts by name regardless of accents or case, falling back to the e-mail', () => {
    const people = buildPeople(
      [
        member({ membership_id: 'a', name: 'zélia' }),
        member({ membership_id: 'b', name: '', email: 'bruno@k3g.com' }),
        member({ membership_id: 'c', name: 'Álvaro' }),
      ],
      [],
    )
    expect(people.map((p) => p.membershipId)).toEqual(['c', 'b', 'a'])
    expect(people[1].name).toBe('bruno@k3g.com')
  })
})

describe('groups', () => {
  it('maps a queue to a group row', () => {
    expect(buildGroups([queue()])[0]).toEqual({
      id: 'q1', name: 'Suporte', mode: 'round_robin', isDefault: true, memberCount: 2, availableCount: 1, openConversationCount: 5,
    })
  })

  it('flags a tenant that has groups but none is the default', () => {
    expect(hasNoDefaultGroup(buildGroups([queue({ is_default: false })]))).toBe(true)
    expect(hasNoDefaultGroup(buildGroups([queue({ is_default: false }), queue({ id: 'q2', is_default: true })]))).toBe(false)
  })

  it('does not nag about a default when there are no groups at all (the empty state covers it)', () => {
    expect(hasNoDefaultGroup([])).toBe(false)
  })
})

describe('pluralize', () => {
  it('uses the singular only for exactly one', () => {
    expect(pluralize(0, 'pessoa', 'pessoas')).toBe('0 pessoas')
    expect(pluralize(1, 'pessoa', 'pessoas')).toBe('1 pessoa')
    expect(pluralize(2, 'pessoa', 'pessoas')).toBe('2 pessoas')
  })
})
