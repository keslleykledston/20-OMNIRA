import { describe, expect, it } from 'vitest'
import { changesOf, describeChange, NO_EDITS, seatOf, type SheetEdits } from '../lib/personDraft'
import type { PersonEntry } from '../lib/peopleModel'

const person = (over: Partial<PersonEntry> = {}): PersonEntry => ({
  key: 'm-ana', name: 'Ana', email: 'ana@k3g.com', roleKey: 'tenant_agent', roleLabel: 'Agente', state: 'active', online: false,
  agent: { profileId: 'p-ana', status: 'active', queues: [{ memberId: 'qm-1', queueId: 'q-sup', queueName: 'Suporte', available: true, capacity: 2 }] },
  ...over,
})
const name = (id: string) => ({ 'q-sup': 'Suporte', 'q-fin': 'Financeiro' })[id] ?? id

describe('alterações pendentes da ficha', () => {
  it('sem edição, nada muda; editar e voltar ao valor salvo também não', () => {
    expect(changesOf(person(), NO_EDITS)).toEqual([])
    const back: SheetEdits = { roleKey: 'tenant_agent', attends: true, seats: { 'q-sup': { member: true, available: true, capacity: 2 } } }
    expect(changesOf(person(), back)).toEqual([])
  })

  it('lista função, filas novas, saída e ajustes, com 0 = sem limite', () => {
    const edits: SheetEdits = { roleKey: 'tenant_supervisor', seats: { 'q-sup': { capacity: 0, available: false }, 'q-fin': { member: true } } }
    const lines = changesOf(person(), edits).map((c) => describeChange(c, name))
    expect(lines).toEqual([
      'Função: Agente → Supervisor',
      'Fila Suporte: fica indisponível; até 2 → sem limite ao mesmo tempo',
      'Entra na fila Financeiro: disponível, até 1 conversa ao mesmo tempo',
    ])
    expect(describeChange({ kind: 'leave', queueId: 'q-sup', memberId: 'qm-1' }, name)).toBe('Sai da fila Suporte')
    expect(describeChange({ kind: 'join', queueId: 'q-fin', available: true, capacity: 0 }, name)).toBe('Entra na fila Financeiro: disponível, sem limite de conversas ao mesmo tempo')
  })

  it('pausar aplica antes as edições de fila feitas enquanto atendia, e a pausa vem por último', () => {
    const edits: SheetEdits = { attends: false, seats: { 'q-fin': { member: true } } }
    expect(changesOf(person(), edits)).toEqual([
      { kind: 'join', queueId: 'q-fin', available: true, capacity: 1 },
      { kind: 'attend', on: false, fresh: false },
    ])
    const paused = person({ agent: { profileId: 'p-ana', status: 'disabled', queues: [] } })
    expect(changesOf(paused, edits)).toEqual([]) // stays paused: the hidden queue edit is not applied
  })

  it('um atendente novo que escolheu filas fica só com elas (tirando a fila padrão que o servidor pôs), também ao tentar de novo', () => {
    const retried = person({ agent: { profileId: 'p-ana', status: 'active', queues: [{ memberId: 'qm-def', queueId: 'q-sup', queueName: 'Suporte', available: true, capacity: 5 }] } })
    const edits: SheetEdits = { attends: true, fresh: true, seats: { 'q-fin': { member: true } } }
    expect(changesOf(retried, edits)).toEqual([
      { kind: 'join', queueId: 'q-fin', available: true, capacity: 1 },
      { kind: 'leave', queueId: 'q-sup', memberId: 'qm-def' },
    ])
    expect(changesOf(retried, { attends: true, fresh: true, seats: {} })).toEqual([]) // chose nothing: keeps the default queue
  })

  it('quem não atende começa sem filas; ao ligar, as filas escolhidas entram', () => {
    const fresh = person({ agent: null })
    expect(seatOf(fresh, NO_EDITS, 'q-sup')).toEqual({ member: false, available: true, capacity: 1 })
    expect(changesOf(fresh, { attends: true, seats: { 'q-sup': { member: true } } })).toEqual([
      { kind: 'attend', on: true, fresh: true },
      { kind: 'join', queueId: 'q-sup', available: true, capacity: 1 },
    ])
  })
})
