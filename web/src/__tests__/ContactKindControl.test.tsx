import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import { ContactKindControl } from '../components/contacts/ContactKindControl'
import { setSession } from './testUtils'
import type { CompanySuggestion, ContactAccountLink, ContactKind } from '../lib/contacts'

vi.mock('axios')

const CONTACT = 'ct-1'

const link = (over: Partial<ContactAccountLink> = {}): ContactAccountLink => ({
  id: 'l1',
  account_id: 'a1',
  account_name: 'ACME Telecom',
  relationship_type: 'employee',
  status: 'active',
  primary: true,
  source: 'manual',
  created_at: '2026-10-05T10:00:00Z',
  ...over,
})

let links: ContactAccountLink[] = []
let suggestions: CompanySuggestion[] = []

function setup(kind: ContactKind, over: { name?: string } = {}) {
  const onChanged = vi.fn()
  render(<ContactKindControl contactId={CONTACT} kind={kind} contactName={over.name ?? 'Maria'} onChanged={onChanged} />)
  return { onChanged }
}

const putBody = () => vi.mocked(axios.put).mock.calls.at(-1)?.[1] as { kind: string; accounts?: Record<string, unknown>[] } | undefined

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
  links = []
  suggestions = []
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/company-suggestions')) return { data: { items: suggestions } }
    if (url.endsWith('/classification')) return { data: { kind: 'customer', classification_source: 'manual', classified_at: null, accounts: links } }
    if (url.endsWith('/crm/companies')) return { data: { items: [{ id: '42', name: 'ACME Telecom', cnpj: '11.111.111/0001-11' }, { id: '77', name: 'XPTO Redes' }] } }
    if (url.endsWith('/accounts')) return { data: { items: [{ id: 'loc-1', name: 'Empresa Local', account_type: 'customer', status: 'active' }] } }
    return { data: {} }
  })
  vi.mocked(axios.put).mockResolvedValue({ data: {} })
  vi.mocked(axios.post).mockResolvedValue({ data: {} })
})

describe('ContactKindControl — Outros and unclassified', () => {
  it('offers only Cliente and Outros; no agent kind exists (staff are Users)', () => {
    setup('unclassified')
    expect(screen.getAllByRole('button').slice(0, 2).map((b) => b.textContent)).toEqual(['Cliente', 'Outros'])
    expect(screen.queryByRole('button', { name: 'Agente' })).not.toBeInTheDocument()
    expect(screen.getByText(/Ainda não classificado/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Cliente' })).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByRole('button', { name: 'Outros' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('saves Outros through the classification API', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Outros' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('other'))
    expect(vi.mocked(axios.put).mock.calls[0][0]).toMatch(new RegExp(`/contacts/${CONTACT}/classification$`))
    expect(putBody()).toEqual({ kind: 'other' })
  })

  it('does nothing when the kind is already the current one', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('other')
    await user.click(screen.getByRole('button', { name: 'Outros' }))
    expect(axios.put).not.toHaveBeenCalled()
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('shows what went wrong and does not report a change when the API refuses', async () => {
    const user = userEvent.setup()
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 403 } })
    const { onChanged } = setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Outros' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Você não tem permissão para classificar contatos.')
    expect(onChanged).not.toHaveBeenCalled()
  })
})

describe('ContactKindControl — becoming a customer needs a company', () => {
  it('Cliente opens the picker and sends nothing until a company is confirmed', async () => {
    const user = userEvent.setup()
    setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    expect(await screen.findByRole('group', { name: 'Empresa do cliente' })).toBeInTheDocument()
    expect(axios.put).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Confirmar' })).toBeDisabled()
  })

  it('sends a directory company by id only (never its name or CNPJ) with the chosen relationship', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    await user.click(await screen.findByRole('button', { name: /ACME Telecom/ }))
    await user.selectOptions(screen.getByLabelText('Vínculo'), 'billing_contact')
    await user.click(screen.getByRole('button', { name: 'Confirmar' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('customer'))
    expect(putBody()).toEqual({
      kind: 'customer',
      accounts: [{ directory_company_id: '42', relationship_type: 'billing_contact', primary: true }],
    })
    expect(JSON.stringify(putBody())).not.toMatch(/ACME|11\.111/)
  })

  it('can pick an existing local account and filter by text', async () => {
    const user = userEvent.setup()
    setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    await screen.findByRole('button', { name: /ACME Telecom/ })
    await user.type(screen.getByLabelText('Buscar empresa'), 'local')
    expect(screen.queryByRole('button', { name: /ACME Telecom/ })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Empresa Local/ }))
    await user.click(screen.getByRole('button', { name: 'Confirmar' }))
    await waitFor(() => expect(putBody()?.accounts?.[0]).toMatchObject({ account_id: 'loc-1' }))
  })

  it('says the directory is unavailable but still lists existing accounts', async () => {
    const user = userEvent.setup()
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/crm/companies')) throw { response: { status: 503 } }
      if (url.endsWith('/accounts')) return { data: { items: [{ id: 'loc-1', name: 'Empresa Local', account_type: 'customer', status: 'active' }] } }
      return { data: {} }
    })
    setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    expect(await screen.findByText(/Diretório de empresas indisponível/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Empresa Local/ })).toBeInTheDocument()
  })

  it('explains a refused customer (422) and keeps the picker open', async () => {
    const user = userEvent.setup()
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 422 } })
    const { onChanged } = setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    await user.click(await screen.findByRole('button', { name: /XPTO Redes/ }))
    await user.click(screen.getByRole('button', { name: 'Confirmar' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('precisa de pelo menos uma empresa')
    expect(onChanged).not.toHaveBeenCalled()
  })
})

describe('ContactKindControl — suggestions from integration evidence', () => {
  const suggestion = (over: Partial<CompanySuggestion> = {}): CompanySuggestion => ({
    evidence_id: 'ev-1',
    external_company_id: '77',
    connection_id: 'conn-1',
    source: 'ticket_selection',
    first_verified_at: '2026-10-01T10:00:00Z',
    last_verified_at: '2026-10-02T10:00:00Z',
    already_linked: false,
    ...over,
  })

  it('lists a suggestion first, named by the directory, and sends the evidence id with the directory id only', async () => {
    suggestions = [suggestion()]
    const user = userEvent.setup()
    const { onChanged } = setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    const options = await screen.findAllByRole('option')
    expect(options[0]).toHaveTextContent('XPTO Redes')
    expect(options[0]).toHaveTextContent('Sugerida por chamado anterior')
    // the directory company is not listed twice
    expect(screen.getAllByRole('button', { name: /XPTO Redes/ })).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: /XPTO Redes/ }))
    await user.click(screen.getByRole('button', { name: 'Confirmar' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('customer'))
    expect(putBody()?.accounts?.[0]).toMatchObject({ directory_company_id: '77', evidence_id: 'ev-1' })
    expect(JSON.stringify(putBody())).not.toMatch(/XPTO/)
  })

  it('a suggestion alone changes nothing: nothing is sent until the person confirms', async () => {
    suggestions = [suggestion()]
    const user = userEvent.setup()
    setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    await screen.findAllByRole('option')
    expect(axios.put).not.toHaveBeenCalled()
    expect(axios.post).not.toHaveBeenCalled()
  })

  it('hides a suggestion that is already linked and survives a failing suggestions request', async () => {
    suggestions = [suggestion({ already_linked: true })]
    const user = userEvent.setup()
    setup('unclassified')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    await screen.findAllByRole('option')
    expect(screen.queryByText('Sugerida por chamado anterior')).not.toBeInTheDocument()

    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/company-suggestions')) throw { response: { status: 500 } }
      if (url.endsWith('/crm/companies')) return { data: { items: [{ id: '42', name: 'ACME Telecom' }] } }
      if (url.endsWith('/accounts')) return { data: { items: [] } }
      return { data: {} }
    })
    const second = userEvent.setup()
    setup('other')
    await second.click(screen.getAllByRole('button', { name: 'Cliente' })[1])
    expect((await screen.findAllByRole('button', { name: /ACME Telecom/ })).length).toBeGreaterThan(0)
  })
})

describe('ContactKindControl — a customer with several companies', () => {
  it('lists the companies, marks the primary and adds another one', async () => {
    links = [link(), link({ id: 'l2', account_id: 'a2', account_name: 'XPTO Redes', primary: false, relationship_type: 'owner' })]
    const user = userEvent.setup()
    setup('customer')
    const list = await screen.findByLabelText('Empresas do cliente')
    expect(within(list).getByText('ACME Telecom')).toBeInTheDocument()
    expect(within(list).getByText('XPTO Redes')).toBeInTheDocument()
    expect(within(list).getAllByText('Principal')).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: '+ Adicionar empresa' }))
    await user.click(await screen.findByRole('button', { name: /Empresa Local/ }))
    await user.click(screen.getByRole('button', { name: 'Confirmar' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(new RegExp(`/contacts/${CONTACT}/accounts$`))
    expect(vi.mocked(axios.post).mock.calls[0][1]).toMatchObject({ account_id: 'loc-1', primary: false })
  })

  it('makes another company the primary one', async () => {
    links = [link(), link({ id: 'l2', account_name: 'XPTO Redes', primary: false })]
    const user = userEvent.setup()
    setup('customer')
    await user.click(await screen.findByRole('button', { name: 'Tornar principal' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/accounts\/l2\/primary$/)
  })

  it('removing one of several companies asks first and ends only that link', async () => {
    links = [link(), link({ id: 'l2', account_name: 'XPTO Redes', primary: false })]
    const user = userEvent.setup()
    const { onChanged } = setup('customer')
    await user.click(await screen.findByRole('button', { name: 'Remover XPTO Redes' }))
    expect(await screen.findByText('Remover empresa?')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Remover' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/accounts\/l2\/end$/)
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({})
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })

  it('removing the LAST company requires choosing how to reclassify the contact', async () => {
    links = [link()]
    const user = userEvent.setup()
    const { onChanged } = setup('customer')
    await user.click(await screen.findByRole('button', { name: 'Remover ACME Telecom' }))
    const dialog = await screen.findByRole('dialog', { name: 'Última empresa' })
    expect(axios.post).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Remover e classificar como Outros' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('other'))
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ reclassify_to: 'other' })
  })

  it('cancelling the last-company dialog changes nothing', async () => {
    links = [link()]
    const user = userEvent.setup()
    setup('customer')
    await user.click(await screen.findByRole('button', { name: 'Remover ACME Telecom' }))
    const dialog = await screen.findByRole('dialog', { name: 'Última empresa' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancelar' }))
    expect(axios.post).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog', { name: 'Última empresa' })).not.toBeInTheDocument()
  })
})

describe('ContactKindControl — spam', () => {
  it('asks before marking spam, and cancelling changes nothing', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('other', { name: 'Promoção Chata' })
    await user.click(screen.getByRole('button', { name: 'Marcar como spam ou golpe' }))
    expect(await screen.findByText('Marcar como spam?')).toBeInTheDocument()
    expect(screen.getByText(/Promoção Chata deixa a fila/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancelar' }))
    expect(axios.put).not.toHaveBeenCalled()
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('marks spam only after the confirmation', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('other')
    await user.click(screen.getByRole('button', { name: 'Marcar como spam ou golpe' }))
    await user.click(await screen.findByRole('button', { name: 'Marcar como spam' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('spam'))
    expect(putBody()).toEqual({ kind: 'spam' })
  })

  it('in the spam state offers to undo it (a false positive), never to mark it again', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('spam')
    expect(screen.getByText('Marcado como spam')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Marcar como spam ou golpe' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Não é spam' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('other'))
    expect(putBody()).toEqual({ kind: 'other' })
  })

  it('restoring a spam contact as customer still asks for a company', async () => {
    const user = userEvent.setup()
    setup('spam')
    await user.click(screen.getByRole('button', { name: 'É cliente' }))
    expect(await screen.findByRole('group', { name: 'Empresa do cliente' })).toBeInTheDocument()
    expect(axios.put).not.toHaveBeenCalled()
  })

  it('a failed spam request keeps the contact as it was and says so', async () => {
    const user = userEvent.setup()
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 500 } })
    const { onChanged } = setup('other')
    await user.click(screen.getByRole('button', { name: 'Marcar como spam ou golpe' }))
    await user.click(await screen.findByRole('button', { name: 'Marcar como spam' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Não foi possível salvar')
    expect(onChanged).not.toHaveBeenCalled()
    expect(screen.queryByText('Marcar como spam?')).not.toBeInTheDocument()
  })
})
