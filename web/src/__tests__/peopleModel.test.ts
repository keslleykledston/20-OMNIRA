import { describe, expect, it } from 'vitest'
import {
  buildPeople,
  buildQueueRows,
  filterPeople,
  hasNoDefaultQueue,
  NO_FILTER,
  pluralize,
  readinessOf,
  summarize,
  type PersonEntry,
} from '../lib/peopleModel'
import type { OperationalAgent } from '../lib/agents'
import type { Invitation } from '../lib/invitations'
import type { Queue } from '../lib/queues'
import type { TeamMember } from '../lib/team'

const member = (over: Partial<TeamMember> = {}): TeamMember => ({
  membership_id: 'm1', user_id: 'u1', name: 'Ana Souza', email: 'ana@k3g.com', role_key: 'tenant_agent',
  role_name: 'Tenant Agent', status: 'active', created_at: '2026-01-01T00:00:00Z', ...over,
})
const agent = (over: Partial<OperationalAgent> = {}): OperationalAgent => ({
  id: 'p1', membership_id: 'm1', user_id: 'u1', name: 'Ana Souza', email: 'ana@k3g.com', role: 'Agente', status: 'active',
  queues: [{ id: 'qm1', queue_id: 'q1', queue_name: 'Suporte', available: true, capacity: 3 }], ...over,
})
const invitation = (over: Partial<Invitation> = {}): Invitation => ({
  id: 'i1', email: 'novo@k3g.com', role_key: 'tenant_agent', role_name: 'Tenant Agent', status: 'pending',
  created_by_email: 'admin@k3g.com', expires_at: '2099-01-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z', sent_at: '2026-01-01T00:01:00Z', ...over,
})
const queue = (over: Partial<Queue> = {}): Queue => ({
  id: 'q1', name: 'Suporte', mode: 'round_robin', is_default: true, member_count: 2, available_count: 1,
  open_conversation_count: 5, created_at: '', updated_at: '', ...over,
})
const one = (m: TeamMember[], a: OperationalAgent[] = [], i: Invitation[] = [], online?: Set<string>): PersonEntry => buildPeople(m, a, i, online)[0]

describe('buildPeople', () => {
  it('joins a member with the agent profile and the queues they attend, and knows who is online', () => {
    const p = one([member()], [agent()], [], new Set(['p1']))
    expect(p).toMatchObject({ key: 'm1', name: 'Ana Souza', roleLabel: 'Agente', state: 'active', online: true })
    expect(p.agent).toEqual({ profileId: 'p1', status: 'active', queues: [{ memberId: 'qm1', queueId: 'q1', queueName: 'Suporte', available: true, capacity: 3 }] })
  })

  it('leaves a member without a profile as someone who does not attend', () => {
    expect(one([member()]).agent).toBeNull()
  })

  it('lists pending, expired and cancelled invitations as their own rows, never an accepted one', () => {
    const people = buildPeople([], [], [
      invitation({ id: 'a', email: 'a@k3g.com' }),
      invitation({ id: 'b', email: 'b@k3g.com', status: 'expired' }),
      invitation({ id: 'c', email: 'c@k3g.com', status: 'revoked' }),
      invitation({ id: 'd', email: 'd@k3g.com', status: 'accepted' }),
    ])
    expect(people.map((p) => [p.key, p.state])).toEqual([['inv:a', 'invited'], ['inv:b', 'invite_expired'], ['inv:c', 'invite_revoked']])
    expect(people[0].roleLabel).toBe('Agente')
  })

  it('sorts members and invitations together by name, ignoring accents and case, falling back to the e-mail', () => {
    const people = buildPeople(
      [member({ membership_id: 'z', name: 'zélia' }), member({ membership_id: 'b', name: '', email: 'bruno@k3g.com' }), member({ membership_id: 'a', name: 'Álvaro' })],
      [],
      [invitation({ email: 'carla@k3g.com' })],
    )
    expect(people.map((p) => p.name)).toEqual(['Álvaro', 'bruno@k3g.com', 'carla@k3g.com', 'zélia'])
  })
})

describe('readinessOf', () => {
  it('says the person receives conversations only when active, in a queue and available in one', () => {
    expect(readinessOf(one([member()], [agent()]))).toEqual({ tone: 'ok', label: 'Recebe conversas' })
  })

  it('points at what is missing', () => {
    expect(readinessOf(one([member()], [agent({ queues: [] })]))).toMatchObject({ tone: 'warn', label: 'Sem fila' })
    expect(readinessOf(one([member()], [agent({ queues: [{ id: 'x', queue_id: 'q1', queue_name: 'S', available: false, capacity: 1 }] })]))).toMatchObject({ tone: 'warn', label: 'Indisponível' })
  })

  it('tells apart someone paused, someone who does not attend and someone whose access is off', () => {
    expect(readinessOf(one([member()], [agent({ status: 'disabled' })])).label).toBe('Em pausa')
    expect(readinessOf(one([member()])).label).toBe('Não atende')
    expect(readinessOf(one([member({ status: 'inactive' })], [agent()])).label).toBe('Acesso suspenso')
    expect(readinessOf(one([member({ status: 'revoked' })])).label).toBe('Acesso removido')
  })

  it('flags an invitation that was never delivered or that expired', () => {
    expect(readinessOf(one([], [], [invitation()]))).toEqual({ tone: 'info', label: 'Convite enviado' })
    expect(readinessOf(one([], [], [invitation({ sent_at: undefined })]))).toMatchObject({ tone: 'warn', label: 'Convite não entregue' })
    expect(readinessOf(one([], [], [invitation({ status: 'expired' })]))).toMatchObject({ tone: 'warn', label: 'Convite vencido' })
  })
})

describe('filterPeople and summarize', () => {
  const people = buildPeople(
    [
      member({ membership_id: 'ok', name: 'Ana', email: 'ana@k3g.com' }),
      member({ membership_id: 'nq', user_id: 'u2', name: 'Bruno', email: 'bruno@k3g.com', role_key: 'tenant_admin' }),
      member({ membership_id: 'rv', user_id: 'u3', name: 'Rui', email: 'rui@k3g.com', status: 'revoked' }),
    ],
    [agent({ membership_id: 'ok' }), agent({ id: 'p2', membership_id: 'nq', queues: [] })],
    [invitation({ email: 'carla@k3g.com' })],
  )

  it('hides removed people unless the history is asked for', () => {
    expect(filterPeople(people, NO_FILTER).map((p) => p.name)).toEqual(['Ana', 'Bruno', 'carla@k3g.com'])
    expect(filterPeople(people, { ...NO_FILTER, history: true }).map((p) => p.name)).toContain('Rui')
  })

  it('filters by situation, function, queue and text (name, e-mail or queue name)', () => {
    expect(filterPeople(people, { ...NO_FILTER, tone: 'warn' }).map((p) => p.name)).toEqual(['Bruno'])
    expect(filterPeople(people, { ...NO_FILTER, role: 'tenant_admin' }).map((p) => p.name)).toEqual(['Bruno'])
    expect(filterPeople(people, { ...NO_FILTER, queue: 'q1' }).map((p) => p.name)).toEqual(['Ana'])
    expect(filterPeople(people, { ...NO_FILTER, text: 'suporte' }).map((p) => p.name)).toEqual(['Ana'])
    expect(filterPeople(people, { ...NO_FILTER, text: 'CARLA' }).map((p) => p.name)).toEqual(['carla@k3g.com'])
  })

  it('counts each situation without the history', () => {
    expect(summarize(people)).toEqual({ all: 3, ok: 1, warn: 1, info: 1, neutral: 0 })
  })
})

describe('queues', () => {
  it('maps a queue to a row', () => {
    expect(buildQueueRows([queue()])[0]).toEqual({
      id: 'q1', name: 'Suporte', mode: 'round_robin', isDefault: true, memberCount: 2, availableCount: 1, openConversationCount: 5,
    })
  })

  it('flags an instance that has queues but none is the default', () => {
    expect(hasNoDefaultQueue(buildQueueRows([queue({ is_default: false })]))).toBe(true)
    expect(hasNoDefaultQueue(buildQueueRows([queue({ is_default: false }), queue({ id: 'q2', is_default: true })]))).toBe(false)
    expect(hasNoDefaultQueue([])).toBe(false)
  })
})

describe('pluralize', () => {
  it('uses the singular only for exactly one', () => {
    expect(pluralize(0, 'pessoa', 'pessoas')).toBe('0 pessoas')
    expect(pluralize(1, 'pessoa', 'pessoas')).toBe('1 pessoa')
    expect(pluralize(2, 'pessoa', 'pessoas')).toBe('2 pessoas')
  })
})
