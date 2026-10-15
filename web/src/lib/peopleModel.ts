import type { OperationalAgent } from './agents'
import type { Invitation } from './invitations'
import type { Queue } from './queues'
import type { TeamMember } from './team'
import { ROLE_DISPLAY_NAME } from './roles'

// The "Pessoas" screen works with one row per person: a member of this instance, or an invitation that is not a member yet.
export interface PersonQueue {
  memberId: string
  queueId: string
  queueName: string
  available: boolean
  capacity: number
}

export interface PersonAgent {
  profileId: string
  status: 'active' | 'disabled'
  queues: PersonQueue[]
}

export type PersonState = 'active' | 'inactive' | 'revoked' | 'invited' | 'invite_expired' | 'invite_revoked'

export interface PersonEntry {
  key: string
  name: string
  email: string
  roleKey: string
  roleLabel: string
  state: PersonState
  member?: TeamMember
  invitation?: Invitation
  agent: PersonAgent | null
  online: boolean
}

export type Tone = 'ok' | 'warn' | 'info' | 'neutral'

export interface Readiness {
  tone: Tone
  label: string
  reason?: string
}

const byName = (a: PersonEntry, b: PersonEntry) => a.name.localeCompare(b.name, 'pt-BR', { sensitivity: 'base' })

function agentOf(a: OperationalAgent | undefined): PersonAgent | null {
  if (!a) return null
  return {
    profileId: a.id,
    status: a.status,
    queues: a.queues.map((q) => ({ memberId: q.id, queueId: q.queue_id, queueName: q.queue_name, available: q.available, capacity: q.capacity })),
  }
}

const INVITATION_STATE: Partial<Record<Invitation['status'], PersonState>> = {
  pending: 'invited',
  expired: 'invite_expired',
  revoked: 'invite_revoked',
}

// An accepted invitation is the member row itself, so it never becomes a second row.
export function buildPeople(
  members: TeamMember[],
  agents: OperationalAgent[],
  invitations: Invitation[],
  onlineProfileIds: ReadonlySet<string> = new Set(),
): PersonEntry[] {
  const profileByMembership = new Map(agents.map((a) => [a.membership_id, a]))
  const people: PersonEntry[] = members.map((m) => {
    const agent = agentOf(profileByMembership.get(m.membership_id))
    return {
      key: m.membership_id,
      name: m.name || m.email,
      email: m.email,
      roleKey: m.role_key,
      roleLabel: ROLE_DISPLAY_NAME[m.role_key] ?? m.role_name,
      state: m.status,
      member: m,
      agent,
      online: agent ? onlineProfileIds.has(agent.profileId) : false,
    }
  })
  for (const inv of invitations) {
    const state = INVITATION_STATE[inv.status]
    if (!state) continue
    people.push({
      key: `inv:${inv.id}`,
      name: inv.email,
      email: inv.email,
      roleKey: inv.role_key,
      roleLabel: ROLE_DISPLAY_NAME[inv.role_key] ?? inv.role_name,
      state,
      invitation: inv,
      agent: null,
      online: false,
    })
  }
  return people.sort(byName)
}

export const isInvitation = (p: PersonEntry) => p.invitation !== undefined

// Removed members and cancelled invitations stay out of the list unless asked for.
export const isHistory = (p: PersonEntry) => p.state === 'revoked' || p.state === 'invite_revoked'

export function readinessOf(p: PersonEntry): Readiness {
  switch (p.state) {
    case 'invited':
      return p.invitation?.sent_at
        ? { tone: 'info', label: 'Convite enviado' }
        : { tone: 'warn', label: 'Convite não entregue', reason: 'O e-mail não chegou: reenvie o convite' }
    case 'invite_expired':
      return { tone: 'warn', label: 'Convite vencido', reason: 'Reenvie para a pessoa conseguir entrar' }
    case 'invite_revoked':
      return { tone: 'neutral', label: 'Convite cancelado' }
    case 'revoked':
      return { tone: 'neutral', label: 'Acesso removido' }
    case 'inactive':
      return { tone: 'neutral', label: 'Acesso suspenso' }
  }
  if (!p.agent) return { tone: 'neutral', label: 'Não atende' }
  if (p.agent.status === 'disabled') return { tone: 'neutral', label: 'Em pausa' }
  if (p.agent.queues.length === 0) return { tone: 'warn', label: 'Sem fila', reason: 'Falta escolher ao menos uma fila' }
  if (!p.agent.queues.some((q) => q.available)) return { tone: 'warn', label: 'Indisponível', reason: 'Indisponível em todas as filas' }
  return { tone: 'ok', label: 'Recebe conversas' }
}

export interface PeopleFilter {
  tone: Tone | 'all'
  role: string
  queue: string
  text: string
  history: boolean
}

export const NO_FILTER: PeopleFilter = { tone: 'all', role: '', queue: '', text: '', history: false }

export function filterPeople(people: PersonEntry[], f: PeopleFilter): PersonEntry[] {
  const text = f.text.trim().toLowerCase()
  return people.filter((p) => {
    if (!f.history && isHistory(p)) return false
    if (f.tone !== 'all' && readinessOf(p).tone !== f.tone) return false
    if (f.role && p.roleKey !== f.role) return false
    if (f.queue && !p.agent?.queues.some((q) => q.queueId === f.queue)) return false
    if (!text) return true
    return [p.name, p.email, ...(p.agent?.queues.map((q) => q.queueName) ?? [])].some((s) => s.toLowerCase().includes(text))
  })
}

export function summarize(people: PersonEntry[]): Record<Tone | 'all', number> {
  const counts = { all: 0, ok: 0, warn: 0, info: 0, neutral: 0 }
  for (const p of people) {
    if (isHistory(p)) continue
    counts.all++
    counts[readinessOf(p).tone]++
  }
  return counts
}

export interface QueueRow {
  id: string
  name: string
  mode: Queue['mode']
  isDefault: boolean
  memberCount: number
  availableCount: number
  openConversationCount: number
}

export function buildQueueRows(queues: Queue[]): QueueRow[] {
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

// New conversations go ONLY to the default queue; with none, they are not distributed at all.
export function hasNoDefaultQueue(queues: QueueRow[]): boolean {
  return queues.length > 0 && !queues.some((q) => q.isDefault)
}

export function pluralize(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}
