import type { CalendarSpec, Interval, OverflowPolicy, OverflowStep, Weekday, WeeklyDay } from './sectors'

// Modelo puro dos horários e do transbordo: só rotula, resume e monta rascunhos. As regras (turno que atravessa a meia-noite, horário de
// verão, feriado, exceção, suspensão) são do servidor; o que a interface mostra como "aberto agora" vem sempre da API.

export const WEEKDAYS: { key: Weekday; short: string; long: string }[] = [
  { key: 'mon', short: 'Seg', long: 'Segunda-feira' },
  { key: 'tue', short: 'Ter', long: 'Terça-feira' },
  { key: 'wed', short: 'Qua', long: 'Quarta-feira' },
  { key: 'thu', short: 'Qui', long: 'Quinta-feira' },
  { key: 'fri', short: 'Sex', long: 'Sexta-feira' },
  { key: 'sat', short: 'Sáb', long: 'Sábado' },
  { key: 'sun', short: 'Dom', long: 'Domingo' },
]

export const DEFAULT_TIMEZONE = 'America/Manaus'

export const TIMEZONES: { id: string; label: string }[] = [
  { id: 'America/Manaus', label: 'Manaus (UTC−4)' },
  { id: 'America/Sao_Paulo', label: 'São Paulo / Brasília (UTC−3)' },
  { id: 'America/Rio_Branco', label: 'Rio Branco (UTC−5)' },
  { id: 'America/Noronha', label: 'Fernando de Noronha (UTC−2)' },
  { id: 'UTC', label: 'UTC' },
]

export const KIND_LABELS: Record<string, string> = { '24x7': '24 horas, todos os dias', business: 'Horário comercial', custom: 'Personalizado' }

export function emptyCalendar(kind: CalendarSpec['kind'], timezone = DEFAULT_TIMEZONE): CalendarSpec {
  if (kind === '24x7') return { kind, timezone }
  const weekdays: Weekday[] = ['mon', 'tue', 'wed', 'thu', 'fri']
  return { kind, timezone, weekly: weekdays.map((day) => ({ day, intervals: [{ start: '08:00', end: '18:00' }] })) }
}

export function minutes(clock: string): number | null {
  if (clock === '24:00') return 1440
  const m = /^(\d{2}):(\d{2})$/.exec(clock)
  if (!m) return null
  const h = Number(m[1])
  const min = Number(m[2])
  return h <= 23 && min <= 59 ? h * 60 + min : null
}

export const crossesMidnight = (i: Interval): boolean => {
  const s = minutes(i.start)
  const e = minutes(i.end)
  return s !== null && e !== null && e <= s
}

export function describeInterval(i: Interval): string {
  return `${i.start}–${i.end}${crossesMidnight(i) ? ' (até o dia seguinte)' : ''}`
}

// "Seg–Sex 08:00–18:00; Sáb 08:00–12:00": dias consecutivos com a mesma faixa viram um intervalo de dias.
export function describeWeekly(weekly: WeeklyDay[] | undefined): string {
  const byDay = new Map<Weekday, string>()
  for (const d of weekly ?? []) if (d.intervals.length) byDay.set(d.day, d.intervals.map(describeInterval).join(' e '))
  const groups: { from: number; to: number; text: string }[] = []
  WEEKDAYS.forEach((w, idx) => {
    const text = byDay.get(w.key)
    if (!text) return
    const last = groups[groups.length - 1]
    if (last && last.to === idx - 1 && last.text === text) last.to = idx
    else groups.push({ from: idx, to: idx, text })
  })
  if (!groups.length) return 'Fechado todos os dias'
  return groups.map((g) => `${g.from === g.to ? WEEKDAYS[g.from].short : `${WEEKDAYS[g.from].short}–${WEEKDAYS[g.to].short}`} ${g.text}`).join('; ')
}

export function describeCalendar(spec: CalendarSpec): string {
  if (spec.kind === '24x7') return KIND_LABELS['24x7']
  return describeWeekly(spec.weekly)
}

// Validação leve para avisar antes de enviar; o servidor é quem decide (e repete todas estas regras).
export function calendarProblems(name: string, spec: CalendarSpec): string[] {
  const out: string[] = []
  if (name.trim().length < 1 || name.trim().length > 60) out.push('O nome precisa ter de 1 a 60 caracteres.')
  if (!spec.timezone) out.push('Escolha o fuso horário.')
  if (spec.kind !== '24x7') {
    const days = (spec.weekly ?? []).filter((d) => d.intervals.length > 0)
    if (!days.length) out.push('Marque ao menos um dia de atendimento.')
    for (const d of spec.weekly ?? []) {
      const label = WEEKDAYS.find((w) => w.key === d.day)?.long ?? d.day
      for (const iv of d.intervals) {
        if (minutes(iv.start) === null || (iv.start === '24:00') || minutes(iv.end) === null) out.push(`${label}: horário inválido.`)
        else if (iv.start === iv.end) out.push(`${label}: início e fim não podem ser iguais.`)
      }
      const sorted = d.intervals.map((iv) => [minutes(iv.start) ?? 0, minutes(iv.end) ?? 0] as const).filter(([s, e]) => e > s).sort((a, b) => a[0] - b[0])
      for (let i = 1; i < sorted.length; i++) if (sorted[i][0] < sorted[i - 1][1]) out.push(`${label}: faixas se sobrepõem.`)
    }
  }
  return out
}

export function setDayIntervals(spec: CalendarSpec, day: Weekday, intervals: Interval[]): CalendarSpec {
  const weekly = (spec.weekly ?? []).filter((d) => d.day !== day)
  if (intervals.length) weekly.push({ day, intervals })
  weekly.sort((a, b) => WEEKDAYS.findIndex((w) => w.key === a.day) - WEEKDAYS.findIndex((w) => w.key === b.day))
  return { ...spec, weekly }
}

// ---- transbordo -------------------------------------------------------------------------------------------------------

export const TRIGGER_LABELS: Record<string, string> = {
  after_hours: 'Fora do expediente',
  no_eligible_agent: 'Sem atendente elegível',
  capacity_exhausted: 'Capacidade esgotada',
  max_wait: 'Tempo máximo de espera',
  sla_risk: 'SLA em risco',
  sla_breach: 'SLA violado',
}

export const DEST_LABELS: Record<OverflowStep['destination_kind'], string> = {
  queue: 'Outro grupo',
  oncall: 'Plantão',
  agent: 'Atendente específico',
  sector: 'Outro setor',
  hub: 'Hub (suporte contratado)',
}

export const EXHAUSTED_LABELS: Record<OverflowPolicy['on_exhausted'], string> = {
  keep_waiting: 'Continuar aguardando na fila',
  contingency: 'Acionar contingência',
  manual: 'Exigir ação manual de uma pessoa',
}

export function describeWait(seconds: number, business: boolean): string {
  if (seconds <= 0) return 'imediatamente'
  const unit = seconds % 3600 === 0 ? `${seconds / 3600} h` : seconds % 60 === 0 ? `${seconds / 60} min` : `${seconds} s`
  return `após ${unit}${business ? ' úteis' : ''}`
}

export function emptyStep(position: number, destination: Pick<OverflowStep, 'destination_kind' | 'destination_id'>): OverflowStep {
  return { position, triggers: ['after_hours', 'no_eligible_agent'], wait_seconds: 0, wait_business: false, allow_closed: false, allow_off_shift: false, ...destination }
}

// Mantém as posições 1..n em ordem (o servidor exige) depois de inserir, remover ou mover etapas.
export function renumber(steps: OverflowStep[]): OverflowStep[] {
  return steps.map((s, i) => ({ ...s, position: i + 1 }))
}

export function moveStep(steps: OverflowStep[], from: number, to: number): OverflowStep[] {
  if (to < 0 || to >= steps.length || from === to) return steps
  const next = [...steps]
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item)
  return renumber(next)
}

export function policyProblems(p: OverflowPolicy, originQueue: string): string[] {
  const out: string[] = []
  if (!p.steps.length) out.push('Inclua ao menos uma etapa.')
  if (p.steps.length > p.max_steps) out.push(`O limite de etapas por conversa é ${p.max_steps}; a lista tem ${p.steps.length}.`)
  const seen = new Set<string>([`queue:${originQueue}`])
  p.steps.forEach((s, i) => {
    const n = i + 1
    if (!s.triggers.length) out.push(`Etapa ${n}: escolha ao menos um gatilho.`)
    if (!s.destination_id) out.push(`Etapa ${n}: escolha o destino.`)
    if (s.triggers.includes('max_wait') && s.wait_seconds <= 0) out.push(`Etapa ${n}: o gatilho de tempo máximo precisa de uma espera maior que zero.`)
    const key = `${s.destination_kind}:${s.destination_id}`
    if (s.destination_id && seen.has(key)) out.push(`Etapa ${n}: o destino repete o grupo de origem ou uma etapa anterior (a conversa voltaria a um lugar onde já esteve).`)
    seen.add(key)
  })
  return out
}

export function formatSeconds(total: number | undefined): string {
  if (!total) return ''
  if (total % 3600 === 0) return `${total / 3600} h`
  if (total % 60 === 0) return `${total / 60} min`
  return `${total} s`
}

// "2026-10-14T09:00" digitado no fuso do HORÁRIO (não do navegador) -> instante ISO. Usado nas suspensões, que são instantes: a pessoa pensa
// na hora do atendimento, não na do computador dela. Resolve o deslocamento do fuso (inclusive horário de verão) com Intl, sem biblioteca.
export function zonedToIso(local: string, timeZone: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(local)
  if (!m) return ''
  const [y, mo, d, h, mi] = m.slice(1).map(Number)
  const wall = Date.UTC(y, mo - 1, d, h, mi)
  const offsetAt = (ts: number) => {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }).formatToParts(new Date(ts))
    const get = (k: string) => Number(parts.find((p) => p.type === k)?.value)
    return Date.UTC(get('year'), get('month') - 1, get('day'), get('hour'), get('minute'), get('second')) - ts
  }
  let guess = wall - offsetAt(wall)
  guess = wall - offsetAt(guess)
  return new Date(guess).toISOString()
}

// ---- conversa: roteamento e SLA ----------------------------------------------------------------------------------------------

export const ENGINE_STATE_LABELS: Record<string, string> = {
  queued: 'Na fila, aguardando um atendente',
  waiting_open: 'Aguardando o setor abrir',
  manual_required: 'Precisa da ação de uma pessoa',
  assigned: 'Com um atendente',
  done: 'Encerrada',
}

export const DECISION_LABELS: Record<string, string> = {
  ASSIGN_AGENT: 'Atribuída a um atendente',
  ENQUEUE: 'Mantida na fila',
  OVERFLOW: 'Movida pelo transbordo',
  WAIT_UNTIL_OPEN: 'Aguardando a abertura',
  EXECUTE_FLOW: 'Fluxo acionado',
  ESCALATE: 'Escalada',
  REQUIRE_MANUAL_ACTION: 'Ação manual necessária',
  NO_ACTION: 'Nada a fazer',
}

export const REASON_LABELS: Record<string, string> = {
  already_assigned: 'já tinha dono',
  no_queue: 'sem grupo',
  conversation_closed: 'conversa encerrada',
  manual_queue: 'grupo de atribuição manual',
  sector_closed: 'setor fechado',
  never_opens: 'o setor não abre dentro do horizonte',
  no_eligible_agent: 'nenhum atendente elegível',
  capacity_exhausted: 'capacidade esgotada',
  assigned: 'atendente escolhido',
  presence_unavailable: 'presença indisponível',
  candidates_truncated: 'lista de atendentes truncada',
  chain_exhausted: 'etapas do transbordo esgotadas',
  overflow_oncall: 'plantão',
  overflow_agent: 'atendente específico',
}

export const METRIC_LABELS: Record<string, string> = {
  first_response: 'Primeira resposta',
  queue_wait: 'Espera na fila',
  assignment: 'Atribuição',
  resolution: 'Resolução',
}

export function reasonLabel(reason: string): string {
  if (REASON_LABELS[reason]) return REASON_LABELS[reason]
  const m = /^(sla_risk|sla_breach):(.+)$/.exec(reason)
  if (m) return `${m[1] === 'sla_risk' ? 'SLA em risco' : 'SLA violado'} (${METRIC_LABELS[m[2]] ?? m[2]})`
  return reason.replace(/_/g, ' ')
}

// "12 min", "2 h 5 min", "1 d 3 h": durações curtas para o painel (arredonda para baixo).
export function humanDuration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  if (s < 60) return 'menos de 1 min'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return h > 0 ? `${d} d ${h} h` : `${d} d`
  if (h > 0) return m > 0 ? `${h} h ${m} min` : `${h} h`
  return `${m} min`
}

// Situação do relógio de SLA agora: tempo que falta até o prazo, ou quanto já passou.
export function slaStatus(c: { state: string; deadline_at: string; risk_at: string }, now: Date): { tone: 'success' | 'warning' | 'danger' | 'default'; text: string } {
  if (c.state === 'met') return { tone: 'success', text: 'cumprida' }
  if (c.state === 'breached') return { tone: 'danger', text: 'violada' }
  const left = (new Date(c.deadline_at).getTime() - now.getTime()) / 1000
  if (left <= 0) return { tone: 'danger', text: `vencida há ${humanDuration(-left)}` }
  const risk = new Date(c.risk_at).getTime() <= now.getTime()
  return { tone: risk ? 'warning' : 'default', text: `faltam ${humanDuration(left)}${risk ? ' · em risco' : ''}` }
}
