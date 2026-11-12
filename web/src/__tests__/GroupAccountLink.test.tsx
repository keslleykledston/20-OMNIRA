import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import { GroupAccountLink } from '../components/groups/GroupAccountLink'
import type { Group } from '../lib/groups'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

// ADR-0059 addendum 3: a groups-only tenant (customer_accounts starts empty — no contacts/tickets ever
// created a local account) needs to link a group straight to a company in its own CRM, not only to an
// already-registered local account. GroupAccountLink merges both sources (same approach as
// ContactKindControl's CompanyPicker) into one search.

const group = (over: Partial<Group> = {}): Group => ({
  id: 'g-1',
  name: 'Grupo de Trabalho',
  enabled: true,
  purpose: 'other',
  account_id: null,
  account_name: null,
  additional_accounts: [],
  connection_id: 'c-1',
  connection_account: '559200000000',
  ai_observer_enabled: false,
  is_alert_channel: false,
  last_message_at: null,
  last_message: null,
  ...over,
})

let localAccounts: Array<{ id: string; name: string; account_type: string; status: string }> = []
let directoryCompanies: Array<{ id: string; name: string; cnpj?: string }> = []
let directoryFails = false

function serveGet() {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/crm/companies')) {
      if (directoryFails) throw { response: { status: 503 } }
      return { data: { items: directoryCompanies } }
    }
    if (url.endsWith('/accounts')) return { data: { items: localAccounts } }
    return { data: {} }
  })
}

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
  localAccounts = []
  directoryCompanies = []
  directoryFails = false
  serveGet()
  vi.mocked(axios.put).mockResolvedValue({ data: group({ account_id: 'a-1', account_name: 'Conta Vinculada' }) })
  vi.mocked(axios.post).mockResolvedValue({ data: group({ additional_accounts: [{ id: 'a-2', name: 'IP Fibra' }] }) })
  vi.mocked(axios.delete).mockResolvedValue({ data: group() })
})

function setup(over: Partial<Group> = {}, canManage = true) {
  return renderAt(<GroupAccountLink group={group(over)} canManage={canManage} />)
}

describe('GroupAccountLink — collapsed state', () => {
  it('shows "Sem cliente vinculado" and a Vincular action when manageable', () => {
    setup()
    expect(screen.getByText('Sem cliente vinculado')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Vincular' })).toBeInTheDocument()
  })

  it('hides every action for someone without group.manage', () => {
    setup({}, false)
    expect(screen.queryByRole('button', { name: 'Vincular' })).not.toBeInTheDocument()
  })

  it('shows the linked account name and offers Alterar/Remover', () => {
    setup({ account_id: 'a-1', account_name: 'ACME Telecom' })
    expect(screen.getByText('ACME Telecom')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Alterar' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remover' })).toBeInTheDocument()
  })

  it('Remover calls the unlink endpoint', async () => {
    const user = userEvent.setup()
    setup({ account_id: 'a-1', account_name: 'ACME Telecom' })
    await user.click(screen.getByRole('button', { name: 'Remover' }))
    await waitFor(() => expect(axios.delete).toHaveBeenCalled())
    expect(vi.mocked(axios.delete).mock.calls[0][0]).toMatch(/\/groups\/g-1\/account$/)
  })
})

describe('GroupAccountLink — linking from local accounts and the CRM directory', () => {
  it('merges the CRM directory and local accounts into one list', async () => {
    directoryCompanies = [{ id: '42', name: 'ACME Telecom', cnpj: '11.111.111/0001-11' }]
    localAccounts = [{ id: 'loc-1', name: 'Empresa Local', account_type: 'customer', status: 'active' }]
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    const list = await screen.findByRole('listbox', { name: 'Empresas' })
    expect(within(list).getByRole('button', { name: /ACME Telecom/ })).toBeInTheDocument()
    expect(within(list).getByRole('button', { name: /Empresa Local/ })).toBeInTheDocument()
  })

  it('a local account whose name already appears in the directory is not listed twice', async () => {
    directoryCompanies = [{ id: '42', name: 'ACME Telecom' }]
    localAccounts = [{ id: 'loc-1', name: 'ACME Telecom', account_type: 'customer', status: 'active' }]
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    const list = await screen.findByRole('listbox', { name: 'Empresas' })
    expect(within(list).getAllByRole('button', { name: /ACME Telecom/ })).toHaveLength(1)
  })

  it('picking a CRM directory company links by directory_company_id only (never its name or CNPJ)', async () => {
    directoryCompanies = [{ id: '42', name: 'ACME Telecom', cnpj: '11.111.111/0001-11' }]
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    await user.click(await screen.findByRole('button', { name: /ACME Telecom/ }))
    await waitFor(() => expect(axios.put).toHaveBeenCalled())
    expect(vi.mocked(axios.put).mock.calls[0][0]).toMatch(/\/groups\/g-1\/account$/)
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ directory_company_id: '42' })
  })

  it('picking a local account links by account_id', async () => {
    localAccounts = [{ id: 'loc-1', name: 'Empresa Local', account_type: 'customer', status: 'active' }]
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    await user.click(await screen.findByRole('button', { name: /Empresa Local/ }))
    await waitFor(() => expect(axios.put).toHaveBeenCalled())
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ account_id: 'loc-1' })
  })

  it('closes the picker and clears the query after a successful link', async () => {
    directoryCompanies = [{ id: '42', name: 'ACME Telecom' }]
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    await user.click(await screen.findByRole('button', { name: /ACME Telecom/ }))
    await waitFor(() => expect(screen.queryByRole('listbox', { name: 'Empresas' })).not.toBeInTheDocument())
  })

  it('Cancelar closes the picker without linking anything', async () => {
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    await screen.findByRole('listbox', { name: 'Empresas' })
    await user.click(screen.getByRole('button', { name: 'Cancelar' }))
    expect(screen.queryByRole('listbox', { name: 'Empresas' })).not.toBeInTheDocument()
    expect(axios.put).not.toHaveBeenCalled()
  })

  it('filters both the directory and local accounts by the same text', async () => {
    directoryCompanies = [
      { id: '42', name: 'ACME Telecom' },
      { id: '77', name: 'XPTO Redes' },
    ]
    localAccounts = [{ id: 'loc-1', name: 'Empresa Local', account_type: 'customer', status: 'active' }]
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    await screen.findByRole('button', { name: /ACME Telecom/ })
    await user.type(screen.getByLabelText('Buscar cliente final'), 'ACME')
    await waitFor(() => expect(screen.queryByRole('button', { name: /XPTO Redes/ })).not.toBeInTheDocument())
    expect(screen.getByRole('button', { name: /ACME Telecom/ })).toBeInTheDocument()
  })

  it('warns when the CRM directory is unavailable but still offers local accounts', async () => {
    directoryFails = true
    localAccounts = [{ id: 'loc-1', name: 'Empresa Local', account_type: 'customer', status: 'active' }]
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    expect(await screen.findByText(/CRM indisponível/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Empresa Local/ })).toBeInTheDocument()
  })

  it('shows nothing-found when both sources are empty', async () => {
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    expect(await screen.findByText('Nenhum cliente encontrado.')).toBeInTheDocument()
  })

  it('shows what the server refused when a directory pick is rejected (422)', async () => {
    directoryCompanies = [{ id: '42', name: 'ACME Telecom' }]
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 422, data: 'company not found or inactive in the directory' } })
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    await user.click(await screen.findByRole('button', { name: /ACME Telecom/ }))
    expect(await screen.findByText(/não foi encontrada ou está inativa no CRM/)).toBeInTheDocument()
    // the picker stays open so the operator can try another company
    expect(screen.getByRole('listbox', { name: 'Empresas' })).toBeInTheDocument()
  })

  it('reports when the CRM directory is not configured for this tenant (503)', async () => {
    directoryCompanies = [{ id: '42', name: 'ACME Telecom' }]
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 503, data: 'company directory is not configured' } })
    const user = userEvent.setup()
    setup()
    await user.click(screen.getByRole('button', { name: 'Vincular' }))
    await user.click(await screen.findByRole('button', { name: /ACME Telecom/ }))
    expect(await screen.findByText(/diretório de empresas do CRM não está disponível/)).toBeInTheDocument()
  })
})

// ADR-0059 addendum 8: some groups genuinely concern more than one CRM client at once (e.g. a
// "swap"/peering group between two of the tenant's own clients). additional_accounts is a separate list
// from the one primary account_id above — never confused with it in the UI or the API call it makes.
describe('GroupAccountLink — additional (non-primary) accounts', () => {
  it('shows every additional account as its own badge, alongside (not instead of) the primary', () => {
    setup({ account_id: 'a-1', account_name: 'InfoRR Consultoria', additional_accounts: [{ id: 'a-2', name: 'IP Fibra' }] })
    expect(screen.getByText('InfoRR Consultoria')).toBeInTheDocument()
    expect(screen.getByText('IP Fibra')).toBeInTheDocument()
  })

  it('hides "+ Adicionar outro cliente" and every Remover for someone without group.manage', () => {
    setup({ additional_accounts: [{ id: 'a-2', name: 'IP Fibra' }] }, false)
    expect(screen.queryByText('+ Adicionar outro cliente')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Remover' })).not.toBeInTheDocument()
  })

  it('"+ Adicionar outro cliente" calls the additional-account endpoint, never the primary one', async () => {
    directoryCompanies = [{ id: '42', name: 'IP Fibra' }]
    const user = userEvent.setup()
    setup({ account_id: 'a-1', account_name: 'InfoRR Consultoria' })
    await user.click(screen.getByText('+ Adicionar outro cliente'))
    await user.click(await screen.findByRole('button', { name: /IP Fibra/ }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/groups\/g-1\/accounts$/)
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ directory_company_id: '42' })
    expect(axios.put).not.toHaveBeenCalled()
  })

  it('an account already linked (primary or additional) is never offered again in "Adicionar outro cliente"', async () => {
    localAccounts = [
      { id: 'a-1', name: 'InfoRR Consultoria', account_type: 'customer', status: 'active' },
      { id: 'a-2', name: 'IP Fibra', account_type: 'customer', status: 'active' },
      { id: 'a-3', name: 'SIDI', account_type: 'customer', status: 'active' },
    ]
    const user = userEvent.setup()
    setup({ account_id: 'a-1', account_name: 'InfoRR Consultoria', additional_accounts: [{ id: 'a-2', name: 'IP Fibra' }] })
    await user.click(screen.getByText('+ Adicionar outro cliente'))
    const list = await screen.findByRole('listbox', { name: 'Empresas' })
    expect(within(list).queryByRole('button', { name: /InfoRR Consultoria/ })).not.toBeInTheDocument()
    expect(within(list).queryByRole('button', { name: /IP Fibra/ })).not.toBeInTheDocument()
    expect(within(list).getByRole('button', { name: /SIDI/ })).toBeInTheDocument()
  })

  it('Remover on an additional account calls DELETE .../accounts/{id}, never touching the primary', async () => {
    const user = userEvent.setup()
    setup({ account_id: 'a-1', account_name: 'InfoRR Consultoria', additional_accounts: [{ id: 'a-2', name: 'IP Fibra' }] })
    const row = screen.getByText('IP Fibra').closest('li')!
    await user.click(within(row).getByRole('button', { name: 'Remover' }))
    await waitFor(() => expect(axios.delete).toHaveBeenCalled())
    expect(vi.mocked(axios.delete).mock.calls[0][0]).toMatch(/\/groups\/g-1\/accounts\/a-2$/)
  })
})
