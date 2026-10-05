import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import Accounts from '../pages/Accounts'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const ACME = { id: 'a1', name: 'ACME Telecom', account_type: 'customer', status: 'active', created_at: '2026-09-01T10:00:00Z', updated_at: '2026-09-01T10:00:00Z' }
const XPTO = { id: 'a2', name: 'XPTO Redes', account_type: 'partner', status: 'inactive', created_at: '2026-09-02T10:00:00Z', updated_at: '2026-09-02T10:00:00Z' }
const detail = (a: typeof ACME, over: object = {}) => ({
  ...a,
  external_links: [{ id: 'l1', provider: 'k3g', connection_id: 'cn', external_company_id: '42', external_name_snapshot: 'ACME TELECOM LTDA', status: 'active', source: 'directory_selection' }],
  ...over,
})

let access = ['account.read', 'account.manage', 'ticket.read']
let accounts = [ACME, XPTO]
let tickets: object[] = [{ id: 't1', conversation_id: 'c1', subject: 'Link caiu', status: 'open', priority: 'high', provider: 'k3g', external_ticket_id: '28180', created_at: '2026-10-01T10:00:00Z', updated_at: '2026-10-01T10:00:00Z' }]

const err = (status: number) => Promise.reject({ response: { status, data: '' } })

beforeEach(() => {
  vi.resetAllMocks()
  setSession()
  access = ['account.read', 'account.manage', 'ticket.read']
  accounts = [ACME, XPTO]
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    if (url.endsWith('/me/access')) return { data: { permissions: access } }
    if (url.endsWith('/accounts/a1/tickets')) return { data: { items: tickets } }
    if (url.endsWith('/accounts/a1')) return { data: detail(ACME) }
    if (url.endsWith('/accounts/a2')) return { data: detail(XPTO, { external_links: [] }) }
    if (url.endsWith('/accounts')) {
      const st = config?.params?.status
      return { data: { items: accounts.filter((a) => !st || a.status === st) } }
    }
    return err(404)
  })
})

const calls = () => vi.mocked(axios.get).mock.calls.filter(([u]) => String(u).endsWith('/accounts')).map(([, c]) => (c as any)?.params ?? {})

describe('Accounts (Empresas) — the real customer accounts page (ADR-0018)', () => {
  it('lists the tenant\'s companies with type and status, from the API (no fixtures)', async () => {
    renderAt(<Accounts />)
    expect(await screen.findByRole('heading', { name: 'Empresas' })).toBeInTheDocument()
    const list = await screen.findByRole('list', { name: 'Empresas' })
    expect(within(list).getByText('ACME Telecom')).toBeInTheDocument()
    expect(within(list).getByText('XPTO Redes')).toBeInTheDocument()
    expect(within(list).getByText('Ativa')).toBeInTheDocument()
    expect(within(list).getByText('Inativa')).toBeInTheDocument()
    expect(screen.queryByText(/Test Company LTDA/)).not.toBeInTheDocument()
    expect(screen.queryByText('Este recurso ainda não está disponível nesta implantação.')).not.toBeInTheDocument()
  })

  it('searches and filters on the server', async () => {
    const user = userEvent.setup()
    renderAt(<Accounts />)
    await screen.findByRole('list', { name: 'Empresas' })
    await user.type(screen.getByLabelText('Buscar empresa'), 'acme')
    await waitFor(() => expect(calls().some((p) => p.q === 'acme')).toBe(true))
    await user.selectOptions(screen.getByLabelText('Status'), 'archived')
    await waitFor(() => expect(calls().some((p) => p.status === 'archived')).toBe(true))
    expect(await screen.findByText('Nenhuma empresa encontrada')).toBeInTheDocument()
  })

  it('opens a company: its CRM links and its tickets', async () => {
    const user = userEvent.setup()
    renderAt(<Accounts />)
    await user.click(await screen.findByRole('button', { name: /ACME Telecom/ }))
    const panel = await screen.findByRole('region', { name: 'Empresa ACME Telecom' })
    expect(await within(panel).findByText('ACME TELECOM LTDA')).toBeInTheDocument()
    expect(within(panel).getByText(/k3g · id 42/)).toBeInTheDocument()
    expect(await within(panel).findByText('Link caiu')).toBeInTheDocument()
    expect(within(panel).getByText('#28180')).toBeInTheDocument()
  })

  it('says a company exists only in OMNIRA when it has no CRM link', async () => {
    const user = userEvent.setup()
    renderAt(<Accounts />)
    await user.click(await screen.findByRole('button', { name: /XPTO Redes/ }))
    expect(await screen.findByText(/Esta empresa existe só no OMNIRA/)).toBeInTheDocument()
  })

  it('renames, deactivates and archives through the API (account.manage)', async () => {
    vi.mocked(axios.patch).mockResolvedValue({ data: ACME })
    const user = userEvent.setup()
    renderAt(<Accounts />)
    await user.click(await screen.findByRole('button', { name: /ACME Telecom/ }))
    await screen.findByRole('region', { name: 'Empresa ACME Telecom' })
    await user.click(screen.getByRole('button', { name: 'Renomear' }))
    const input = screen.getByLabelText('Novo nome')
    await user.clear(input)
    await user.type(input, 'ACME Telecom S.A.')
    await user.click(screen.getByRole('button', { name: 'Salvar' }))
    await waitFor(() => expect(axios.patch).toHaveBeenCalled())
    expect(String(vi.mocked(axios.patch).mock.calls[0][0])).toMatch(/\/accounts\/a1$/)
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ name: 'ACME Telecom S.A.' })
    await user.click(await screen.findByRole('button', { name: 'Inativar' }))
    await waitFor(() => expect(vi.mocked(axios.patch).mock.calls.at(-1)?.[1]).toEqual({ status: 'inactive' }))
    await user.click(screen.getByRole('button', { name: 'Arquivar' }))
    await waitFor(() => expect(vi.mocked(axios.patch).mock.calls.at(-1)?.[1]).toEqual({ status: 'archived' }))
  })

  it('creates a company, which then opens', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: { ...ACME, id: 'a1', name: 'Nova Empresa' } })
    const user = userEvent.setup()
    renderAt(<Accounts />)
    await user.click(await screen.findByRole('button', { name: 'Nova empresa' }))
    await user.type(screen.getByLabelText('Nome da empresa'), 'Nova Empresa')
    await user.selectOptions(screen.getByLabelText('Tipo da empresa'), 'partner')
    await user.click(screen.getByRole('button', { name: 'Criar' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ name: 'Nova Empresa', account_type: 'partner' })
  })

  it('without account.manage nothing can be changed; without ticket.read there is no tickets section', async () => {
    access = ['account.read']
    const user = userEvent.setup()
    renderAt(<Accounts />)
    await user.click(await screen.findByRole('button', { name: /ACME Telecom/ }))
    await screen.findByRole('region', { name: 'Empresa ACME Telecom' })
    expect(screen.queryByRole('button', { name: 'Nova empresa' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Renomear' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Arquivar' })).not.toBeInTheDocument()
    expect(screen.queryByText('Chamados')).not.toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/tickets'))).toBe(false)
  })

  it('without account.read the page is a permission state and asks the API for nothing', async () => {
    access = ['ticket.read']
    renderAt(<Accounts />)
    expect(await screen.findByText('Você não tem permissão para ver as empresas.')).toBeInTheDocument()
    expect(calls()).toHaveLength(0)
  })

  it('shows a refused change', async () => {
    vi.mocked(axios.patch).mockImplementation(() => err(403))
    const user = userEvent.setup()
    renderAt(<Accounts />)
    await user.click(await screen.findByRole('button', { name: /ACME Telecom/ }))
    await user.click(await screen.findByRole('button', { name: 'Arquivar' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Você não tem permissão para esta ação.')
  })
})
