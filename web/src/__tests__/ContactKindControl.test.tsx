import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import { ContactKindControl } from '../components/contacts/ContactKindControl'
import { setSession } from './testUtils'
import type { ContactKind } from '../lib/contacts'

vi.mock('axios')

const CONTACT = 'ct-1'

function setup(kind: ContactKind, over: { name?: string } = {}) {
  const onChanged = vi.fn()
  render(<ContactKindControl contactId={CONTACT} kind={kind} contactName={over.name ?? 'Maria'} onChanged={onChanged} />)
  return { onChanged }
}

const patchedKind = () => (vi.mocked(axios.patch).mock.calls.at(-1)?.[1] as { kind: string } | undefined)?.kind

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
  vi.mocked(axios.patch).mockResolvedValue({ data: {} })
})

describe('ContactKindControl — customer / other', () => {
  it('shows the current kind as pressed and saves a change through the contacts API', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('other')
    expect(screen.getByRole('button', { name: 'Outros' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Cliente' })).toHaveAttribute('aria-pressed', 'false')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('customer'))
    expect(vi.mocked(axios.patch).mock.calls[0][0]).toMatch(new RegExp(`/contacts/${CONTACT}$`))
    expect(patchedKind()).toBe('customer')
  })

  it('does nothing when the kind is already the current one', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('customer')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    expect(axios.patch).not.toHaveBeenCalled()
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('shows what went wrong and does not report a change when the API refuses', async () => {
    const user = userEvent.setup()
    vi.mocked(axios.patch).mockRejectedValue({ response: { status: 403 } })
    const { onChanged } = setup('other')
    await user.click(screen.getByRole('button', { name: 'Cliente' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Você não tem permissão para classificar contatos.')
    expect(onChanged).not.toHaveBeenCalled()
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
    expect(axios.patch).not.toHaveBeenCalled()
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('marks spam only after the confirmation', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('customer')
    await user.click(screen.getByRole('button', { name: 'Marcar como spam ou golpe' }))
    await user.click(await screen.findByRole('button', { name: 'Marcar como spam' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('spam'))
    expect(patchedKind()).toBe('spam')
  })

  it('in the spam state offers to undo it (a false positive), never to mark it again', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('spam')
    expect(screen.getByText('Marcado como spam')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Marcar como spam ou golpe' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Não é spam' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('other'))
    expect(patchedKind()).toBe('other')
  })

  it('can restore a spam contact straight to customer', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('spam')
    await user.click(screen.getByRole('button', { name: 'É cliente' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('customer'))
  })

  it('a failed spam request keeps the contact as it was and says so', async () => {
    const user = userEvent.setup()
    vi.mocked(axios.patch).mockRejectedValue({ response: { status: 500 } })
    const { onChanged } = setup('other')
    await user.click(screen.getByRole('button', { name: 'Marcar como spam ou golpe' }))
    await user.click(await screen.findByRole('button', { name: 'Marcar como spam' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Não foi possível salvar')
    expect(onChanged).not.toHaveBeenCalled()
    expect(screen.queryByText('Marcar como spam?')).not.toBeInTheDocument()
  })
})

describe('ContactKindControl — agent (K3G team member)', () => {
  it('offers Cliente, Outros and Agente and saves agent through the contacts API', async () => {
    const user = userEvent.setup()
    const { onChanged } = setup('other')
    expect(screen.getAllByRole('button').slice(0, 3).map((b) => b.textContent)).toEqual(['Cliente', 'Outros', 'Agente'])
    await user.click(screen.getByRole('button', { name: 'Agente' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('agent'))
    expect(patchedKind()).toBe('agent')
  })

  it('shows an agent as pressed and says it does not enter the queue', () => {
    setup('agent')
    expect(screen.getByRole('button', { name: 'Agente' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText(/não entra na fila/)).toBeInTheDocument()
  })
})
