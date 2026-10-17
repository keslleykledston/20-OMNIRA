import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import ServiceSessionPanel from '../components/service/ServiceSessionPanel'
import { renderAt, renderAtWithClient, setSession } from './testUtils'

vi.mock('axios')

const CONV = 'c-1'
const session = (over: Record<string, unknown> = {}) => ({
  id: 's-1', protocol: 'AT-10482', external_protocol: null, conversation_id: CONV, contact_id: 'ct-1', customer_id: null, queue_id: null, category_id: null,
  status: 'open', resolution_code: null, resolution_note: '', origin_channel: 'whatsapp', opened_at: '2026-10-15T12:00:00Z', closed_at: null, reopen_count: 0, version: 3, ...over,
})
const task = (over: Record<string, unknown> = {}) => ({
  id: 't-1', title: 'Ligar para o cliente', description: '', status: 'open', priority: 'normal', assigned_user_id: null, due_at: null, overdue: false, session_id: 's-1', case_id: null, version: 1, ...over,
})
const ALL = () => true
const NONE = () => false

function serve(opts: { session?: unknown; tasks?: unknown[]; absent?: boolean } = {}) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith(`/conversations/${CONV}/service-session`)) {
      if (opts.absent || opts.session === undefined) return Promise.reject({ response: { status: 404 } })
      return { data: opts.session }
    }
    if (url.endsWith('/service-tasks')) return { data: { items: opts.tasks ?? [] } }
    if (url.endsWith('/service-result-codes')) return { data: { items: [{ id: 'rc-1', code: 'visita_agendada', canonical_code: 'task_created', label: 'Visita agendada', active: true }] } }
    return Promise.reject({ response: { status: 404 } })
  })
}

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('Painel de atendimento com protocolo (ADR-0049)', () => {
  it('shows the protocol and state of the active session', async () => {
    serve({ session: session({ status: 'in_progress' }) })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    expect(await screen.findByTestId('service-protocol')).toHaveTextContent('AT-10482')
    expect(screen.getByLabelText('Situação do protocolo')).toHaveValue('in_progress')
  })

  it('hides itself when the module is off (404) and the person could not open a session anyway', async () => {
    serve({ absent: true })
    const { container } = renderAt(<ServiceSessionPanel conversationId={CONV} can={NONE} />)
    await waitFor(() => expect(vi.mocked(axios.get)).toHaveBeenCalled())
    expect(container.querySelector('section')).toBeNull()
  })

  it('offers to open one when there is none and the person may create, with an idempotency key', async () => {
    serve()
    vi.mocked(axios.post).mockResolvedValue({ data: session() })
    const user = userEvent.setup()
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    await user.click(await screen.findByRole('button', { name: /Abrir atendimento com protocolo/ }))
    await waitFor(() => expect(vi.mocked(axios.post)).toHaveBeenCalled())
    const [url, body, cfg] = vi.mocked(axios.post).mock.calls[0] as [string, any, any]
    expect(url).toMatch(/\/service-sessions$/)
    expect(body).toEqual({ conversation_id: CONV })
    expect(cfg.headers['Idempotency-Key']).toMatch(/.{8,}/)
  })

  it('does not offer to open one on a closed conversation or without the key', async () => {
    serve()
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} conversationClosed />)
    await waitFor(() => expect(vi.mocked(axios.get)).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: /Abrir atendimento/ })).toBeNull()
  })

  it('has NO separate way to end the protocol: it ends with "Finalizar" (one action), and says so', async () => {
    serve({ session: session() })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    await screen.findByTestId('service-protocol')
    expect(screen.queryByRole('button', { name: /Encerrar atendimento/ })).toBeNull()
    expect(screen.queryByLabelText('Resultado')).toBeNull()
    expect(screen.getByText(/O protocolo encerra junto com o atendimento, em “Finalizar”/)).toBeInTheDocument()
    expect(vi.mocked(axios.post)).not.toHaveBeenCalled()
  })

  it('without service.close there is no way to close or complete', async () => {
    serve({ session: session(), tasks: [task()] })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={(k) => k === 'service.read'} />)
    await screen.findByTestId('service-protocol')
    await screen.findByText('Ligar para o cliente')
    expect(screen.queryByRole('button', { name: /Encerrar atendimento/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Concluir tarefa/ })).toBeNull()
    expect(screen.queryByLabelText('Nova tarefa')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Adicionar tarefa' })).toBeNull()
    expect(screen.queryByLabelText('Situação do protocolo')).toBeNull() // read-only: the situation is only a chip
    expect(screen.getByText('Aberto')).toBeInTheDocument()
  })

  it('a closed session offers no more actions and shows its result', async () => {
    serve({ session: session({ status: 'closed', closed_at: '2026-10-15T13:00:00Z', resolution_canonical: 'information_provided' }) })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    await screen.findByTestId('service-protocol')
    expect(screen.getByText(/Informação prestada/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Encerrar atendimento/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /aguardando/ })).toBeNull()
    expect(screen.queryByLabelText('Situação do protocolo')).toBeNull()
  })

  it('creates and completes tasks of the session', async () => {
    serve({ session: session(), tasks: [task()] })
    vi.mocked(axios.post).mockResolvedValue({ data: task() })
    const user = userEvent.setup()
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    expect(screen.queryByLabelText('Nova tarefa')).toBeNull() // the field is opened on demand
    await user.click(await screen.findByRole('button', { name: 'Adicionar tarefa' }))
    await user.type(screen.getByLabelText('Nova tarefa'), 'Enviar segunda via')
    await user.click(screen.getByRole('button', { name: 'Salvar tarefa' }))
    await waitFor(() => expect(vi.mocked(axios.post)).toHaveBeenCalledTimes(1))
    // saved: the field closes again and the button returns
    expect(await screen.findByRole('button', { name: 'Adicionar tarefa' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Nova tarefa')).toBeNull()
    let [url, body, cfg] = vi.mocked(axios.post).mock.calls[0] as [string, any, any]
    expect(url).toMatch(/\/service-tasks$/)
    expect(body).toEqual({ title: 'Enviar segunda via', session_id: 's-1' })
    expect(cfg.headers['Idempotency-Key']).toBeTruthy()
    await user.click(await screen.findByRole('button', { name: 'Concluir tarefa: Ligar para o cliente' }))
    await waitFor(() => expect(vi.mocked(axios.post)).toHaveBeenCalledTimes(2));
    [url, body, cfg] = vi.mocked(axios.post).mock.calls[1] as [string, any, any]
    expect(url).toMatch(/\/service-tasks\/t-1\/complete$/)
    expect(cfg.headers['If-Match']).toBe('"1"')
  })

  it('a stale screen (412) says so in plain words and reloads', async () => {
    serve({ session: session() })
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 412 } })
    const user = userEvent.setup()
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    await user.selectOptions(await screen.findByLabelText('Situação do protocolo'), 'waiting')
    expect(await screen.findByRole('alert')).toHaveTextContent(/Outra pessoa alterou/)
    await waitFor(() => expect(vi.mocked(axios.get).mock.calls.filter((c) => String(c[0]).endsWith('/service-session')).length).toBeGreaterThan(1))
  })

  it('every cached answer is keyed by the Hub, so two Hubs serving the same company never share it', async () => {
    serve({ session: session(), tasks: [task()] })
    const { queryClient } = renderAtWithClient(<ServiceSessionPanel conversationId={CONV} can={ALL} hub={{ tenantId: 'inst-9', hubId: 'hub-1' }} />)
    await screen.findByText('Ligar para o cliente')
    const keys = queryClient.getQueryCache().getAll().map((q) => JSON.stringify(q.queryKey))
    expect(keys.length).toBeGreaterThanOrEqual(2)
    for (const k of keys) expect(k).toContain('hub-1')
  })

  it('attended through the Hub the call names the Hub and the instance', async () => {
    serve({ session: session() })
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} hub={{ tenantId: 'inst-9', hubId: 'hub-1' }} />)
    await screen.findByTestId('service-protocol')
    const [url, cfg] = vi.mocked(axios.get).mock.calls[0] as [string, any]
    expect(url).toContain('/tenants/inst-9/')
    expect(Object.values(cfg.headers)).toContain('hub:hub-1')
  })

  it('the task field closes and forgets what was typed on Escape and on the cancel button', async () => {
    serve({ session: session() })
    const user = userEvent.setup()
    renderAt(<ServiceSessionPanel conversationId={CONV} can={ALL} />)
    await user.click(await screen.findByRole('button', { name: 'Adicionar tarefa' }))
    await user.type(screen.getByLabelText('Nova tarefa'), 'rascunho{Escape}')
    expect(screen.queryByLabelText('Nova tarefa')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Adicionar tarefa' }))
    expect(screen.getByLabelText('Nova tarefa')).toHaveValue('')
    await user.type(screen.getByLabelText('Nova tarefa'), 'outro')
    await user.click(screen.getByRole('button', { name: 'Cancelar tarefa' }))
    expect(screen.queryByLabelText('Nova tarefa')).toBeNull()
    expect(vi.mocked(axios.post)).not.toHaveBeenCalled()
  })
})
