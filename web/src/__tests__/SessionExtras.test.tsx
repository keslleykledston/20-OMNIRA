import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import ServiceSessionPanel from '../components/service/ServiceSessionPanel'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const CONV = 'c-1'
const session = { id: 's-1', protocol: 'KTG-1005', external_protocol: null, conversation_id: CONV, contact_id: 'ct-1', customer_id: null, queue_id: null, category_id: null, status: 'in_progress', resolution_code: null, resolution_note: '', origin_channel: 'whatsapp', opened_at: '2026-10-15T12:00:00Z', closed_at: null, reopen_count: 0, version: 3 }
const kase = (over: Record<string, unknown> = {}) => ({ id: 'cs-1', case_number: 'OC-1042', title: 'Cabo rompido na rua', status: 'investigating', priority: 'high', origin_session_id: 's-1', opened_at: '2026-10-15T12:10:00Z', version: 1, ...over })
const ALL = () => true

function serve(opts: { cases?: unknown[]; requests?: unknown[] | 'absent'; timeline?: unknown[] } = {}) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith(`/conversations/${CONV}/service-session`)) return { data: session }
    if (url.endsWith('/service-tasks')) return { data: { items: [] } }
    if (url.endsWith('/service-cases')) return { data: { items: opts.cases ?? [] } }
    if (url.endsWith('/external-requests')) return opts.requests === 'absent' ? Promise.reject({ response: { status: 404 } }) : { data: { items: opts.requests ?? [] } }
    if (url.endsWith('/service-sessions/s-1/timeline')) return { data: { items: opts.timeline ?? [] } }
    return Promise.reject({ response: { status: 404 } })
  })
}

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('Atendimento com protocolo — ocorrências, ordens de serviço e histórico', () => {
  it('lists the occurrences opened from the protocol with number and state', async () => {
    serve({ cases: [kase(), kase({ id: 'cs-2', case_number: 'OC-1043', title: 'Troca de roteador', status: 'resolved' })] })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    const box = await screen.findByLabelText('Ocorrências do atendimento')
    expect(await within(box).findByText(/Cabo rompido na rua/)).toBeInTheDocument()
    expect(within(box).getByText('Em investigação')).toBeInTheDocument()
    expect(within(box).getByText('Resolvida')).toBeInTheDocument()
    const call = vi.mocked(axios.get).mock.calls.find(([u]) => String(u).endsWith('/service-cases')) as [string, any]
    expect(call[1].params.session_id).toBe('s-1')
  })

  it('opens an occurrence from the protocol, with an idempotency key, and nothing is sent to an ERP by that', async () => {
    serve()
    vi.mocked(axios.post).mockResolvedValue({ data: kase() })
    const user = userEvent.setup()
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    expect(screen.queryByLabelText('Nova ocorrência')).toBeNull() // opened on demand
    await user.click(await screen.findByRole('button', { name: 'Adicionar ocorrência' }))
    await user.type(screen.getByLabelText('Nova ocorrência'), '  Cabo rompido  ')
    await user.click(screen.getByRole('button', { name: 'Abrir ocorrência' }))
    await waitFor(() => expect(vi.mocked(axios.post)).toHaveBeenCalled())
    expect(await screen.findByRole('button', { name: 'Adicionar ocorrência' })).toBeInTheDocument() // saved: the field closes
    expect(screen.queryByLabelText('Nova ocorrência')).toBeNull()
    const [url, body, cfg] = vi.mocked(axios.post).mock.calls[0] as [string, any, any]
    expect(url).toMatch(/\/service-cases$/)
    expect(body).toEqual({ title: 'Cabo rompido', session_id: 's-1' })
    expect(cfg.headers['Idempotency-Key']).toMatch(/.{8,}/)
  })

  it('the occurrence field closes and forgets what was typed on Escape', async () => {
    serve()
    const user = userEvent.setup()
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    await user.click(await screen.findByRole('button', { name: 'Adicionar ocorrência' }))
    await user.type(screen.getByLabelText('Nova ocorrência'), 'rascunho{Escape}')
    expect(screen.queryByLabelText('Nova ocorrência')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Adicionar ocorrência' }))
    expect(screen.getByLabelText('Nova ocorrência')).toHaveValue('')
    expect(vi.mocked(axios.post)).not.toHaveBeenCalled()
  })

  it('without service.create there is no form to open an occurrence', async () => {
    serve({ cases: [kase()] })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={(k) => k === 'service.read'} />)
    await screen.findByText(/Cabo rompido na rua/)
    expect(screen.queryByLabelText('Nova ocorrência')).not.toBeInTheDocument()
  })

  it('shows the service orders asked of the ERP, and warns when the ERP never answered', async () => {
    serve({ requests: [{ id: 'r-1', title: 'Instalar ONU', state: 'confirmed_success', operation: 'create_service_order', created_at: '2026-10-15T12:20:00Z' }, { id: 'r-2', title: 'Trocar fonte', state: 'outcome_unknown', operation: 'create_service_order', created_at: '2026-10-15T12:30:00Z' }] })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    const box = await screen.findByLabelText('Ordens de serviço no ERP')
    expect(within(box).getByText('Criada no ERP')).toBeInTheDocument()
    expect(within(box).getByText(/Sem resposta do ERP/)).toBeInTheDocument()
  })

  it('shows no service-order block when there is none or the module is off (the common case)', async () => {
    serve({ requests: 'absent' })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    await screen.findByTestId('service-protocol')
    await waitFor(() => expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/external-requests'))).toBe(true))
    expect(screen.queryByLabelText('Ordens de serviço no ERP')).not.toBeInTheDocument()
  })

  it('through the Hub the ERP requests are never asked (they are the company\'s own), but occurrences and history are, naming the company and the Hub', async () => {
    serve({ cases: [kase()] })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} hub={{ hubId: 'hub-1', tenantId: 'tenant-z' }} />)
    await screen.findByText(/Cabo rompido na rua/)
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/external-requests'))).toBe(false)
    const call = vi.mocked(axios.get).mock.calls.find(([u]) => String(u).endsWith('/service-cases')) as [string, any]
    expect(call[0]).toContain('/tenants/tenant-z/')
    expect(call[1].headers['X-Omnira-Acting-As']).toBe('hub:hub-1')
  })

  it('the history is read only when opened and speaks plainly (known events by name, who did it, no message text)', async () => {
    serve({ timeline: [
      { id: 'e1', event_type: 'service_session.closed', occurred_at: '2026-10-15T13:00:00Z', actor_type: 'user' },
      { id: 'e2', event_type: 'conversation.assignment_changed', occurred_at: '2026-10-15T12:30:00Z', actor_type: 'system', derived: true },
      { id: 'e3', event_type: 'service_session.reopened', occurred_at: '2026-10-15T12:45:00Z', actor_type: 'user', note: 'o cliente voltou' },
      { id: 'e4', event_type: 'novo.evento_futuro', occurred_at: '2026-10-15T12:00:00Z', actor_type: 'flow' },
    ] })
    const user = userEvent.setup()
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    await screen.findByTestId('service-protocol')
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/timeline'))).toBe(false)
    await user.click(screen.getByText('Histórico do atendimento'))
    const list = await screen.findByRole('list', { name: 'Histórico do atendimento' })
    expect(within(list).getByText('Protocolo encerrado')).toBeInTheDocument()
    expect(within(list).getByText('Conversa passou para outra pessoa')).toBeInTheDocument()
    expect(within(list).getByText('“o cliente voltou”')).toBeInTheDocument()
    expect(within(list).getByText('novo.evento_futuro')).toBeInTheDocument() // an event type the screen does not know yet is shown, never hidden
  })
})
