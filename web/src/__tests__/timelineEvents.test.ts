import { describe, expect, it } from 'vitest'
import { knownEvents, mergeTimeline, timelineText, type TimelineEvent } from '../lib/timelineEvents'
import type { MessageItem } from '../types/api'

const ev = (over: Partial<TimelineEvent>): TimelineEvent => ({ id: 'e1', kind: 'claimed', at: '2026-10-18T10:00:00Z', ...over })
const msg = (id: string, at: string): MessageItem => ({ id, conversation_id: 'c', body: id, direction: 'inbound', status: 'received', created_at: at })

describe('timelineText', () => {
  it('says "Você" for the reader, the name when the server gave it, and a generic label for a colleague the reader may not look up', () => {
    expect(timelineText(ev({ kind: 'claimed', actor: 'Ana', actor_id: 'u1' }), 'u1')).toBe('Você assumiu o atendimento')
    expect(timelineText(ev({ kind: 'claimed', actor: 'Ana', actor_id: 'u1' }), 'u2')).toBe('Ana assumiu o atendimento')
    expect(timelineText(ev({ kind: 'claimed', actor_id: 'u1' }), 'u2')).toBe('Um atendente assumiu o atendimento')
  })

  it('tells each kind', () => {
    expect(timelineText(ev({ kind: 'transferred', actor: 'Ana', actor_id: 'u1', target: 'Beto', target_id: 'u2' }), 'x')).toBe('Ana transferiu o atendimento para Beto')
    expect(timelineText(ev({ kind: 'assigned', actor: 'Chefe', actor_id: 'c', target_id: 'u2' }), 'u2')).toBe('Chefe atribuiu o atendimento a Você')
    expect(timelineText(ev({ kind: 'released' }), null)).toBe('Atendimento devolvido à fila')
    expect(timelineText(ev({ kind: 'auto_assigned', system: true, target: 'Beto', target_id: 'u2' }), null)).toBe('Distribuído automaticamente para Beto')
    expect(timelineText(ev({ kind: 'external_claimed' }), null)).toBe('Aplicativo externo assumiu o atendimento')
    expect(timelineText(ev({ kind: 'protocol_opened', protocol: 'KTG-1007' }), null)).toBe('Protocolo KTG-1007 aberto')
    expect(timelineText(ev({ kind: 'protocol_closed', protocol: 'KTG-1007' }), null)).toBe('Protocolo KTG-1007 encerrado')
    expect(timelineText(ev({ kind: 'finalized', protocol: 'KTG-1007', detail: 'resolved', actor: 'Ana', actor_id: 'u1' }), 'u2')).toBe('Atendimento finalizado por Ana · Protocolo KTG-1007 · Resolvido')
    expect(timelineText(ev({ kind: 'finalized', detail: 'no_response', system: true }), null)).toBe('Atendimento finalizado automaticamente · Sem resposta do cliente')
  })
})

describe('mergeTimeline', () => {
  const messages = [msg('m1', '2026-10-18T10:00:00Z'), msg('m2', '2026-10-18T10:10:00Z')]

  it('puts each event between the messages around it, a message first on a tie', () => {
    const items = mergeTimeline(messages, [ev({ id: 'tie', at: '2026-10-18T10:00:00Z' }), ev({ id: 'mid', at: '2026-10-18T10:05:00Z' })], false)
    expect(items.map((i) => (i.type === 'message' ? i.message.id : i.event.id))).toEqual(['m1', 'tie', 'mid', 'm2'])
  })

  it('holds back an event older than the oldest loaded message while older messages are still unloaded, and shows it afterwards', () => {
    const old = ev({ id: 'old', at: '2026-10-18T08:00:00Z' })
    expect(mergeTimeline(messages, [old], true).map((i) => i.type)).toEqual(['message', 'message'])
    expect(mergeTimeline(messages, [old], false).map((i) => i.type)).toEqual(['event', 'message', 'message'])
  })

  it('shows the events of an attendance that has no messages yet', () => {
    expect(mergeTimeline([], [ev({})], false)).toHaveLength(1)
  })
})

describe('knownEvents', () => {
  it('keeps what can be drawn and drops what cannot (a kind from a newer server, a malformed row, a non-list)', () => {
    expect(knownEvents([ev({}), { id: 'x', kind: 'new_thing', at: '2026-10-18T10:00:00Z' }, { kind: 'claimed' }, null, ev({ id: 'bad', at: 'nope' })]).map((e) => e.id)).toEqual(['e1'])
    expect(knownEvents({ items: [] })).toEqual([])
  })
})
