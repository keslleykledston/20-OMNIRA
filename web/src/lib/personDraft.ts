import type { PersonEntry, PersonQueue } from './peopleModel'
import { ROLE_DISPLAY_NAME } from './roles'

// The person sheet collects changes and applies them only after "Salvar alterações" and a confirmation (owner, 2026-10-14). Edits are
// kept as overrides on top of what the server says, so after a save that only partly worked the sheet still shows - and offers to
// save - exactly what is left.

export interface SeatChoice {
  member: boolean
  available: boolean
  /** 0 = no limit of simultaneous conversations in the queue. */
  capacity: number
}

export interface SheetEdits {
  roleKey?: string
  attends?: boolean
  seats: Record<string, Partial<SeatChoice>>
}

export const NO_EDITS: SheetEdits = { seats: {} }

export type SheetChange =
  | { kind: 'role'; from: string; to: string }
  | { kind: 'attend'; on: boolean; fresh: boolean }
  | { kind: 'join'; queueId: string; available: boolean; capacity: number }
  | { kind: 'leave'; queueId: string; memberId: string }
  | { kind: 'seat'; queueId: string; memberId: string; available: boolean; capacity: number; before: { available: boolean; capacity: number } }

export function currentSeat(person: PersonEntry, queueId: string): PersonQueue | undefined {
  return person.agent?.queues.find((q) => q.queueId === queueId)
}

/** What the sheet shows for a queue: the edit when there is one, else the server's seat (new seats start available, limit 1). */
export function seatOf(person: PersonEntry, edits: SheetEdits, queueId: string): SeatChoice {
  const cur = currentSeat(person, queueId)
  const e = edits.seats[queueId] ?? {}
  return {
    member: e.member ?? !!cur,
    available: e.available ?? cur?.available ?? true,
    capacity: e.capacity ?? cur?.capacity ?? 1,
  }
}

export function attendsOf(person: PersonEntry, edits: SheetEdits): boolean {
  return edits.attends ?? person.agent?.status === 'active'
}

export function roleOf(person: PersonEntry, edits: SheetEdits): string {
  return edits.roleKey ?? person.roleKey
}

/** Only real differences from the server: an edit that went back to the saved value is no change. */
export function changesOf(person: PersonEntry, edits: SheetEdits): SheetChange[] {
  const out: SheetChange[] = []
  const role = roleOf(person, edits)
  if (role !== person.roleKey) out.push({ kind: 'role', from: person.roleKey, to: role })
  const was = person.agent?.status === 'active'
  const on = attendsOf(person, edits)
  if (on !== was) out.push({ kind: 'attend', on, fresh: !person.agent })
  if (!on) return out // a pause keeps the queues as they are: seat edits are not applied
  for (const queueId of Object.keys(edits.seats)) {
    const cur = currentSeat(person, queueId)
    const s = seatOf(person, edits, queueId)
    if (s.member && !cur) out.push({ kind: 'join', queueId, available: s.available, capacity: s.capacity })
    else if (!s.member && cur) out.push({ kind: 'leave', queueId, memberId: cur.memberId })
    else if (s.member && cur && (s.available !== cur.available || s.capacity !== cur.capacity)) {
      out.push({ kind: 'seat', queueId, memberId: cur.memberId, available: s.available, capacity: s.capacity, before: { available: cur.available, capacity: cur.capacity } })
    }
  }
  return out
}

export function capacityText(n: number): string {
  return n === 0 ? 'sem limite' : String(n)
}

function capacitySentence(n: number): string {
  return n === 0 ? 'sem limite de conversas ao mesmo tempo' : `até ${n} ${n === 1 ? 'conversa' : 'conversas'} ao mesmo tempo`
}

function capacityShort(n: number): string {
  return n === 0 ? 'sem limite' : `até ${n}`
}

const roleName = (key: string) => ROLE_DISPLAY_NAME[key] ?? key

/** One sentence per change, for the confirmation. */
export function describeChange(c: SheetChange, queueName: (id: string) => string): string {
  switch (c.kind) {
    case 'role':
      return `Função: ${roleName(c.from)} → ${roleName(c.to)}`
    case 'attend':
      if (!c.on) return 'Fica em pausa: não recebe conversas novas (as filas ficam guardadas)'
      return c.fresh ? 'Passa a receber conversas' : 'Volta a receber conversas'
    case 'join':
      return `Entra na fila ${queueName(c.queueId)}: ${c.available ? 'disponível' : 'indisponível'}, ${capacitySentence(c.capacity)}`
    case 'leave':
      return `Sai da fila ${queueName(c.queueId)}`
    case 'seat': {
      const parts: string[] = []
      if (c.available !== c.before.available) parts.push(c.available ? 'fica disponível' : 'fica indisponível')
      if (c.capacity !== c.before.capacity) parts.push(`${capacityShort(c.before.capacity)} → ${capacityShort(c.capacity)} ao mesmo tempo`)
      return `Fila ${queueName(c.queueId)}: ${parts.join('; ')}`
    }
  }
}
