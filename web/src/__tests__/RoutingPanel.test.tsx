import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import axios from 'axios'
import RoutingPanel from '../components/inbox/RoutingPanel'
import { humanDuration, reasonLabel, slaStatus } from '../lib/attendanceModel'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const iso = (offsetMin: number) => new Date(Date.now() + offsetMin * 60_000).toISOString()

const routing = (over: object = {}) => ({
  conversation_id: 'c1', status: 'open',
  queue: { id: 'q1', name: 'Cobrança' }, sector: { id: 's1', name: 'Financeiro' },
  engine: { state: 'waiting_open', last_decision: 'WAIT_UNTIL_OPEN', episode: 1, step: 1, hops: 1, entered_queue_at: iso(-75), waiting_seconds: 4500, next_attempt_at: iso(600) },
  schedule: { open: false, at: iso(0), calendar_source: 'sector', timezone: 'America/Manaus', next_open: iso(600) },
  sla: [
    { metric: 'assignment', state: 'running', started_at: iso(-5), risk_at: iso(3), deadline_at: iso(5), counting: 'wall' },
    { metric: 'first_response', state: 'running', started_at: iso(-30), risk_at: iso(-10), deadline_at: iso(20), counting: 'wall' },
    { metric: 'queue_wait', state: 'breached', started_at: iso(-60), risk_at: iso(-30), deadline_at: iso(-10), counting: 'wall' },
  ],
  history: [
    { at: iso(-60), trigger: 'route', decision: 'OVERFLOW', reason: 'chain_exhausted', destination: 'queue:x', destination_label: 'Suporte N2', attempts: 1 },
    { at: iso(-70), trigger: 'route', decision: 'WAIT_UNTIL_OPEN', reason: 'sla_risk:assignment', attempts: 3 },
  ],
  ...over,
})

function show(data: unknown | null) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/routing')) return data === null ? Promise.reject({ response: { status: 500 } }) : { data }
    return Promise.reject({ response: { status: 404 } })
  })
  renderAt(<RoutingPanel conversationId="c1" />, '/x', '/x')
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  setSession()
})

describe('RoutingPanel', () => {
  it('diz onde a conversa está, por que espera, o SLA e o histórico', async () => {
    show(routing())
    const panel = await screen.findByRole('region', { name: 'Roteamento' })
    expect(within(panel).getByText('Financeiro')).toBeInTheDocument()
    expect(within(panel).getByText('Cobrança')).toBeInTheDocument()
    expect(within(panel).getByText('Ninguém ainda')).toBeInTheDocument()
    expect(within(panel).getByText(/Horário: fechado · abre/)).toBeInTheDocument()
    expect(within(panel).getByText('Aguardando o setor abrir')).toBeInTheDocument()
    expect(within(panel).getByText('Espera: 1 h 15 min')).toBeInTheDocument()
    expect(within(panel).getByText(/Já passou por 1 etapa de transbordo/)).toBeInTheDocument()
    // SLA: cada meta com seu estado
    expect(within(panel).getByText('Atribuição').parentElement).toHaveTextContent(/faltam 4 min|faltam 5 min/)
    expect(within(panel).getByText('Primeira resposta').parentElement).toHaveTextContent(/em risco/)
    expect(within(panel).getByText('Espera na fila').parentElement).toHaveTextContent('violada')
    // histórico com destino pelo nome e motivo em português
    const hist = within(panel).getByText(/Histórico de roteamento \(2\)/)
    expect(hist).toBeInTheDocument()
    expect(within(panel).getByText('Movida pelo transbordo')).toBeInTheDocument()
    expect(within(panel).getByText(/Suporte N2/)).toBeInTheDocument()
    expect(within(panel).getByText(/etapas do transbordo esgotadas/)).toBeInTheDocument()
    expect(within(panel).getByText(/SLA em risco \(Atribuição\)/)).toBeInTheDocument()
    expect(within(panel).getByText('3×')).toBeInTheDocument()
  })

  it('com atendente e sem motor mostra só o que existe, sem inventar estado', async () => {
    show(routing({ assigned: { user_id: 'u1', name: 'Ana Souza' }, engine: undefined, sla: [], history: [], schedule: undefined }))
    const panel = await screen.findByRole('region', { name: 'Roteamento' })
    expect(within(panel).getByText('Ana Souza')).toBeInTheDocument()
    expect(within(panel).queryByText(/Aguardando/)).toBeNull()
    expect(within(panel).queryByText('Metas de SLA')).toBeNull()
    expect(within(panel).queryByText(/Histórico de roteamento/)).toBeNull()
  })

  it('falha de leitura vira uma frase honesta, não um painel vazio', async () => {
    show(null)
    expect(await screen.findByText(/não está disponível agora/)).toBeInTheDocument()
  })
})

describe('modelo da conversa', () => {
  it('humanDuration arredonda para baixo e usa a maior unidade', () => {
    expect(humanDuration(30)).toBe('menos de 1 min')
    expect(humanDuration(60)).toBe('1 min')
    expect(humanDuration(3600)).toBe('1 h')
    expect(humanDuration(3 * 3600 + 120)).toBe('3 h 2 min')
    expect(humanDuration(86400 + 3 * 3600)).toBe('1 d 3 h')
    expect(humanDuration(-5)).toBe('menos de 1 min')
  })
  it('slaStatus separa no prazo, em risco, vencida, cumprida e violada', () => {
    const now = new Date('2026-10-13T12:00:00Z')
    const at = (m: number) => new Date(now.getTime() + m * 60000).toISOString()
    expect(slaStatus({ state: 'running', risk_at: at(5), deadline_at: at(10) }, now)).toEqual({ tone: 'default', text: 'faltam 10 min' })
    expect(slaStatus({ state: 'running', risk_at: at(-1), deadline_at: at(10) }, now)).toEqual({ tone: 'warning', text: 'faltam 10 min · em risco' })
    expect(slaStatus({ state: 'running', risk_at: at(-20), deadline_at: at(-2) }, now)).toEqual({ tone: 'danger', text: 'vencida há 2 min' })
    expect(slaStatus({ state: 'met', risk_at: at(0), deadline_at: at(0) }, now).tone).toBe('success')
    expect(slaStatus({ state: 'breached', risk_at: at(0), deadline_at: at(0) }, now).tone).toBe('danger')
  })
  it('reasonLabel traduz os motivos do motor e os de SLA', () => {
    expect(reasonLabel('no_eligible_agent')).toBe('nenhum atendente elegível')
    expect(reasonLabel('sla_breach:first_response')).toBe('SLA violado (Primeira resposta)')
    expect(reasonLabel('algo_novo_do_servidor')).toBe('algo novo do servidor')
  })
})
