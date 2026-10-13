import { describe, expect, it } from 'vitest'
import {
  calendarProblems, crossesMidnight, describeCalendar, describeWait, describeWeekly, emptyCalendar, emptyStep, minutes, moveStep, policyProblems, renumber, setDayIntervals,
} from '../lib/attendanceModel'
import type { OverflowPolicy } from '../lib/sectors'

describe('horários', () => {
  it('resume dias consecutivos com a mesma faixa', () => {
    expect(describeCalendar(emptyCalendar('business'))).toBe('Seg–Sex 08:00–18:00')
    expect(describeCalendar({ kind: '24x7', timezone: 'UTC' })).toBe('24 horas, todos os dias')
    const sat = setDayIntervals(emptyCalendar('business'), 'sat', [{ start: '08:00', end: '12:00' }])
    expect(describeWeekly(sat.weekly)).toBe('Seg–Sex 08:00–18:00; Sáb 08:00–12:00')
    expect(describeWeekly([])).toBe('Fechado todos os dias')
  })

  it('reconhece o turno que atravessa a meia-noite', () => {
    expect(crossesMidnight({ start: '22:00', end: '06:00' })).toBe(true)
    expect(crossesMidnight({ start: '08:00', end: '18:00' })).toBe(false)
    expect(crossesMidnight({ start: '00:00', end: '24:00' })).toBe(false)
    expect(describeWeekly([{ day: 'mon', intervals: [{ start: '22:00', end: '06:00' }] }])).toBe('Seg 22:00–06:00 (até o dia seguinte)')
    expect(minutes('24:00')).toBe(1440)
    expect(minutes('25:00')).toBeNull()
  })

  it('avisa antes de enviar o que o servidor também recusaria', () => {
    expect(calendarProblems('Comercial', emptyCalendar('business'))).toEqual([])
    expect(calendarProblems('', emptyCalendar('business'))[0]).toMatch(/nome/)
    expect(calendarProblems('X', { kind: 'custom', timezone: 'UTC', weekly: [] })).toContain('Marque ao menos um dia de atendimento.')
    const overlap = setDayIntervals(emptyCalendar('business'), 'mon', [{ start: '08:00', end: '12:00' }, { start: '11:00', end: '18:00' }])
    expect(calendarProblems('X', overlap).join(' ')).toMatch(/sobrepõem/)
    const equal = setDayIntervals(emptyCalendar('business'), 'mon', [{ start: '08:00', end: '08:00' }])
    expect(calendarProblems('X', equal).join(' ')).toMatch(/iguais/)
  })

  it('setDayIntervals mantém a ordem da semana e remove o dia sem faixas', () => {
    let spec = emptyCalendar('custom')
    spec = setDayIntervals(spec, 'sun', [{ start: '10:00', end: '14:00' }])
    spec = setDayIntervals(spec, 'wed', [])
    expect(spec.weekly?.map((d) => d.day)).toEqual(['mon', 'tue', 'thu', 'fri', 'sun'])
  })
})

describe('transbordo', () => {
  const policy = (steps: OverflowPolicy['steps']): OverflowPolicy => ({ active: true, max_steps: 4, on_exhausted: 'manual', steps })

  it('descreve a espera e mantém as posições 1..n', () => {
    expect(describeWait(0, false)).toBe('imediatamente')
    expect(describeWait(600, false)).toBe('após 10 min')
    expect(describeWait(7200, true)).toBe('após 2 h úteis')
    const steps = [emptyStep(1, { destination_kind: 'queue', destination_id: 'a' }), emptyStep(2, { destination_kind: 'oncall', destination_id: 'b' })]
    expect(moveStep(steps, 0, 1).map((s) => [s.position, s.destination_id])).toEqual([[1, 'b'], [2, 'a']])
    expect(moveStep(steps, 0, 5)).toBe(steps)
    expect(renumber([{ ...steps[1], position: 9 }])[0].position).toBe(1)
  })

  it('recusa ciclos, destino vazio e tempo máximo sem espera antes de enviar', () => {
    const origin = 'q0'
    const ok = policy([emptyStep(1, { destination_kind: 'queue', destination_id: 'q1' })])
    expect(policyProblems(ok, origin)).toEqual([])
    expect(policyProblems(policy([]), origin)[0]).toMatch(/ao menos uma etapa/)
    expect(policyProblems(policy([emptyStep(1, { destination_kind: 'queue', destination_id: origin })]), origin).join(' ')).toMatch(/voltaria/)
    const dup = policy([emptyStep(1, { destination_kind: 'queue', destination_id: 'q1' }), emptyStep(2, { destination_kind: 'queue', destination_id: 'q1' })])
    expect(policyProblems(dup, origin).join(' ')).toMatch(/Etapa 2/)
    expect(policyProblems(policy([emptyStep(1, { destination_kind: 'queue', destination_id: '' })]), origin).join(' ')).toMatch(/destino/)
    const maxWait = policy([{ ...emptyStep(1, { destination_kind: 'queue', destination_id: 'q1' }), triggers: ['max_wait'], wait_seconds: 0 }])
    expect(policyProblems(maxWait, origin).join(' ')).toMatch(/tempo máximo/)
    expect(policyProblems({ ...ok, max_steps: 1, steps: [...ok.steps, emptyStep(2, { destination_kind: 'queue', destination_id: 'q2' })] }, origin).join(' ')).toMatch(/limite/)
  })
})

describe('zonedToIso', () => {
  it('interpreta a hora digitada no fuso do horário, com horário de verão', async () => {
    const { zonedToIso } = await import('../lib/attendanceModel')
    expect(zonedToIso('2026-10-14T09:00', 'America/Manaus')).toBe('2026-10-14T13:00:00.000Z')
    expect(zonedToIso('2026-07-01T09:00', 'America/New_York')).toBe('2026-07-01T13:00:00.000Z')
    expect(zonedToIso('2026-01-15T09:00', 'America/New_York')).toBe('2026-01-15T14:00:00.000Z')
    expect(zonedToIso('2026-10-14T09:00', 'UTC')).toBe('2026-10-14T09:00:00.000Z')
    expect(zonedToIso('lixo', 'UTC')).toBe('')
  })
})
