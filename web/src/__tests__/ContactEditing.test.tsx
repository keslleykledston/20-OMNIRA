import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import axios from 'axios'
import { ContactDetailsEditor } from '../components/contacts/ContactDetailsEditor'
import { ContactNotes } from '../components/contacts/ContactNotes'
import { ContactKindControl } from '../components/contacts/ContactKindControl'
import { setSession } from './testUtils'

vi.mock('axios')

const ID = 'ct-1'
const wrap = (ui: React.ReactElement) =>
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>)

const contact = (over: object = {}) => ({ id: ID, display_name: 'Fulano', phone_e164: '+5592999990000', email: 'f@x.com', status: 'active', kind: 'other', ...over })
const err = (status: number) => Promise.reject({ response: { status } })

beforeEach(() => {
  vi.resetAllMocks()
  setSession()
})

describe('ContactDetailsEditor — name and e-mail', () => {
  beforeEach(() => {
    vi.mocked(axios.get).mockResolvedValue({ data: contact() })
  })

  it('shows the e-mail and edits name and e-mail through the contacts API (never the phone)', async () => {
    vi.mocked(axios.put).mockResolvedValue({ data: contact({ display_name: 'Fulano da Silva', email: 'novo@x.com' }) })
    const onChanged = vi.fn()
    const user = userEvent.setup()
    wrap(<ContactDetailsEditor contactId={ID} onChanged={onChanged} />)
    expect(await screen.findByText('f@x.com')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Editar contato' }))
    const name = screen.getByLabelText('Nome do contato')
    expect(name).toHaveValue('Fulano')
    await user.clear(name)
    await user.type(name, 'Fulano da Silva')
    const mail = screen.getByLabelText('E-mail do contato')
    await user.clear(mail)
    await user.type(mail, 'novo@x.com')
    await user.click(screen.getByRole('button', { name: 'Salvar' }))
    await waitFor(() => expect(axios.put).toHaveBeenCalled())
    expect(String(vi.mocked(axios.put).mock.calls[0][0])).toMatch(new RegExp(`/contacts/${ID}/details$`))
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ display_name: 'Fulano da Silva', email: 'novo@x.com' })
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(await screen.findByText('novo@x.com')).toBeInTheDocument()
    expect(screen.queryByLabelText('Nome do contato')).not.toBeInTheDocument()
  })

  it('does not save an empty name, and explains a refusal', async () => {
    vi.mocked(axios.put).mockImplementation(() => err(422))
    const user = userEvent.setup()
    wrap(<ContactDetailsEditor contactId={ID} />)
    await user.click(await screen.findByRole('button', { name: 'Editar contato' }))
    const name = screen.getByLabelText('Nome do contato')
    await user.clear(name)
    expect(screen.getByRole('button', { name: 'Salvar' })).toBeDisabled()
    await user.type(name, 'Novo')
    await user.click(screen.getByRole('button', { name: 'Salvar' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Confira os dados')
    expect(screen.getByLabelText('Nome do contato')).toBeInTheDocument()
  })

  it('cancel leaves everything as it was', async () => {
    const user = userEvent.setup()
    wrap(<ContactDetailsEditor contactId={ID} />)
    await user.click(await screen.findByRole('button', { name: 'Editar contato' }))
    await user.type(screen.getByLabelText('Nome do contato'), ' X')
    await user.click(screen.getByRole('button', { name: 'Cancelar' }))
    expect(axios.put).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Editar contato' })).toBeInTheDocument()
  })
})

describe('ContactNotes — comments that document the context', () => {
  const note = (over: object = {}) => ({ id: 'n1', body: 'Prefere WhatsApp de manhã.', author_user_id: 'u1', author_name: 'Ana', created_at: '2026-10-05T10:00:00Z', updated_at: '2026-10-05T10:00:00Z', mine: true, ...over })
  let notes: object[] = []
  beforeEach(() => {
    notes = [note(), note({ id: 'n2', body: 'Contrato renova em outubro.', author_name: 'Beto', mine: false })]
    vi.mocked(axios.get).mockImplementation(async () => ({ data: { items: notes } }))
  })

  it('lists notes with author and time, and offers edit/remove only on the caller\'s own', async () => {
    wrap(<ContactNotes contactId={ID} />)
    const section = await screen.findByRole('region', { name: 'Anotações do contato' })
    expect(await within(section).findByText('Prefere WhatsApp de manhã.')).toBeInTheDocument()
    expect(within(section).getByText(/Ana ·/)).toBeInTheDocument()
    expect(within(section).getByText(/Beto ·/)).toBeInTheDocument()
    expect(within(section).getAllByRole('button', { name: 'Editar' })).toHaveLength(1)
    expect(within(section).getAllByRole('button', { name: 'Remover anotação' })).toHaveLength(1)
  })

  it('adds a note', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: note({ id: 'n3' }) })
    const user = userEvent.setup()
    wrap(<ContactNotes contactId={ID} />)
    await user.type(await screen.findByLabelText('Nova anotação'), '  Cliente VIP  ')
    await user.click(screen.getByRole('button', { name: 'Adicionar anotação' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(String(vi.mocked(axios.post).mock.calls[0][0])).toMatch(new RegExp(`/contacts/${ID}/notes$`))
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ body: 'Cliente VIP' })
    await waitFor(() => expect(screen.getByLabelText('Nova anotação')).toHaveValue(''))
  })

  it('edits and removes the caller\'s own note', async () => {
    vi.mocked(axios.put).mockResolvedValue({ data: note() })
    vi.mocked(axios.delete).mockResolvedValue({ data: {} })
    const user = userEvent.setup()
    wrap(<ContactNotes contactId={ID} />)
    await user.click(await screen.findByRole('button', { name: 'Editar' }))
    const box = screen.getByLabelText('Editar anotação')
    await user.clear(box)
    await user.type(box, 'Prefere WhatsApp à tarde.')
    await user.click(screen.getByRole('button', { name: 'Salvar anotação' }))
    await waitFor(() => expect(axios.put).toHaveBeenCalled())
    expect(String(vi.mocked(axios.put).mock.calls[0][0])).toMatch(/\/notes\/n1$/)
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ body: 'Prefere WhatsApp à tarde.' })
    await user.click(await screen.findByRole('button', { name: 'Remover anotação' }))
    await waitFor(() => expect(axios.delete).toHaveBeenCalled())
    expect(String(vi.mocked(axios.delete).mock.calls[0][0])).toMatch(/\/notes\/n1$/)
  })

  it('does not exist for a role that cannot read notes', async () => {
    vi.mocked(axios.get).mockImplementation(() => err(403))
    wrap(<ContactNotes contactId={ID} />)
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Anotações do contato' })).not.toBeInTheDocument())
    expect(axios.get).toHaveBeenCalled()
  })

  it('shows a refused write', async () => {
    vi.mocked(axios.post).mockImplementation(() => err(403))
    const user = userEvent.setup()
    wrap(<ContactNotes contactId={ID} />)
    await user.type(await screen.findByLabelText('Nova anotação'), 'x')
    await user.click(screen.getByRole('button', { name: 'Adicionar anotação' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Você não tem permissão')
  })
})

describe('ContactKindControl — companies of an "Outros" contact', () => {
  const link = { id: 'l1', account_id: 'a1', account_name: 'ACME Telecom', relationship_type: 'employee', status: 'active', primary: true, source: 'manual', created_at: '2026-10-05T10:00:00Z' }
  beforeEach(() => {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/classification')) return { data: { kind: 'other', classification_source: 'manual', classified_at: null, accounts: [link] } }
      if (url.endsWith('/crm/companies')) return { data: { items: [{ id: '42', name: 'ACME Telecom' }, { id: '77', name: 'XPTO Redes' }] } }
      if (url.endsWith('/accounts')) return { data: { items: [] } }
      if (url.endsWith('/company-suggestions')) return { data: { items: [] } }
      return { data: {} }
    })
  })

  it('lists the companies of an Outros contact and adds one WITHOUT making the contact a customer', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: {} })
    const user = userEvent.setup()
    const onChanged = vi.fn()
    wrap(<ContactKindControl contactId={ID} kind="other" contactName="Fulano" onChanged={onChanged} />)
    const list = await screen.findByLabelText('Empresas do cliente')
    expect(within(list).getByText('ACME Telecom')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '+ Adicionar empresa' }))
    expect(await screen.findByRole('group', { name: 'Adicionar empresa' })).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: /XPTO Redes/ }))
    await user.click(screen.getByRole('button', { name: 'Confirmar' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(String(vi.mocked(axios.post).mock.calls[0][0])).toMatch(new RegExp(`/contacts/${ID}/accounts$`))
    expect(axios.put).not.toHaveBeenCalled() // the kind is not touched
    expect(onChanged).not.toHaveBeenCalledWith('customer')
  })

  it('removing the only company of an Outros contact needs no reclassification', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: {} })
    const user = userEvent.setup()
    wrap(<ContactKindControl contactId={ID} kind="other" contactName="Fulano" onChanged={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Remover ACME Telecom' }))
    expect(screen.queryByRole('dialog', { name: 'Última empresa' })).not.toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: 'Remover' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(String(vi.mocked(axios.post).mock.calls[0][0])).toMatch(/\/accounts\/l1\/end$/)
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({})
  })
})
