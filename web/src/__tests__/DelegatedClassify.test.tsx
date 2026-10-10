import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import axios from 'axios'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import DelegatedContextPane from '../components/inbox/DelegatedContextPane'
import { setActing, clearActing } from '../lib/acting'
import { setSession } from './testUtils'

// Phase 04a: the controls to classify and edit the contact are offered to a Hub agent only when the SERVER says (through /me/access, which answers
// with the DELEGATED keys in this context) that it holds contact.classify. The server still decides every request.
const get = vi.spyOn(axios, 'get')

function serve(keys: string[]) {
  get.mockImplementation(async (url: string) => {
    const u = String(url)
    if (u.endsWith('/me/access')) return { data: { role_key: 'hub_delegate', permissions: keys } }
    if (u.endsWith('/inbox/conversations/c1')) return { data: { id: 'c1', contact_name: 'Berjon Brito', status: 'open', contact_id: 'k1', contact_kind: 'unclassified' } }
    if (u.endsWith('/contacts/k1')) return { data: { id: 'k1', display_name: 'Berjon Brito', kind: 'unclassified' } }
    if (u.endsWith('/accounts')) return { data: { items: [{ id: 'a1', name: 'Empresa A', account_type: 'customer', status: 'active' }] } }
    return Promise.reject({ response: { status: 404 } })
  })
}

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <DelegatedContextPane conversationId="c1" />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  setSession()
  setActing('hub-1', 'Beta Hub')
  get.mockReset()
})
afterEach(() => clearActing())

describe('classifying the contact while attending through the Hub', () => {
  it('offers the kind and the edit controls with contact.classify, without "Interno"', async () => {
    serve(['conversation.read', 'contact.read', 'contact.classify', 'account.read'])
    show()
    expect(await screen.findByRole('button', { name: 'Editar contato' })).toBeInTheDocument()
    const kinds = await screen.findByRole('group', { name: 'Tipo de contato' })
    expect(kinds).toHaveTextContent('Cliente')
    expect(kinds).toHaveTextContent('Outros')
    expect(kinds).not.toHaveTextContent('Interno')
  })

  it("offers the instance's ERP companies, the suggestions and the existing accounts when becoming a customer (phase 04b)", async () => {
    serve(['conversation.read', 'contact.read', 'contact.classify', 'account.read'])
    const base = get.getMockImplementation()!
    get.mockImplementation(async (url: string, ...rest: unknown[]) => {
      const u = String(url)
      if (u.endsWith('/crm/companies')) return { data: { items: [{ id: 'co-1', name: 'Acme Telecom' }] } }
      if (u.includes('company-suggestions')) return { data: { items: [] } }
      return (base as (u: string, ...r: unknown[]) => Promise<unknown>)(url, ...rest)
    })
    show()
    const customer = await screen.findByRole('button', { name: 'Cliente' })
    await act(async () => customer.click())
    expect(await screen.findByText('Acme Telecom')).toBeInTheDocument()
    expect(await screen.findByText('Empresa A')).toBeInTheDocument()
    const asked = get.mock.calls.map((c) => String(c[0]))
    expect(asked.some((u) => u.endsWith('/crm/companies'))).toBe(true)
  })

  it('still offers the existing accounts when the instance has no ERP (the directory answers 503)', async () => {
    serve(['conversation.read', 'contact.read', 'contact.classify', 'account.read'])
    const base = get.getMockImplementation()!
    get.mockImplementation(async (url: string, ...rest: unknown[]) => {
      if (String(url).endsWith('/crm/companies')) return Promise.reject({ response: { status: 503 } })
      return (base as (u: string, ...r: unknown[]) => Promise<unknown>)(url, ...rest)
    })
    show()
    const customer = await screen.findByRole('button', { name: 'Cliente' })
    await act(async () => customer.click())
    expect(await screen.findByText('Empresa A')).toBeInTheDocument()
  })

  it('shows no editing control with only the read keys', async () => {
    serve(['conversation.read', 'contact.read', 'account.read'])
    show()
    expect(await screen.findByText('Berjon Brito')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Editar contato' })).not.toBeInTheDocument()
    expect(screen.queryByRole('group', { name: 'Tipo de contato' })).not.toBeInTheDocument()
  })

  it('shows no editing control when the keys cannot be read at all', async () => {
    get.mockImplementation(async (url: string) => {
      const u = String(url)
      if (u.endsWith('/inbox/conversations/c1')) return { data: { id: 'c1', contact_name: 'Berjon Brito', status: 'open', contact_id: 'k1' } }
      return Promise.reject({ response: { status: 403 } })
    })
    show()
    expect(await screen.findByText('Berjon Brito')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Editar contato' })).not.toBeInTheDocument()
  })
})

describe('the ERP ticket while attending through the Hub (phase 04b)', () => {
  it('offers the ticket panel, without refresh or status change, to a person who holds ticket.create', async () => {
    serve(['conversation.read', 'contact.read', 'ticket.create', 'ticket.read'])
    const base = get.getMockImplementation()!
    get.mockImplementation(async (url: string, ...rest: unknown[]) => {
      const u = String(url)
      if (u.endsWith('/conversations/c1/ticket')) {
        return { data: { local_ticket_id: 't1', linked: true, provider: 'k3g', external_ticket_id: '28180', external_status_label: 'Novo', sync_status: 'synced' } }
      }
      return (base as (u: string, ...r: unknown[]) => Promise<unknown>)(url, ...rest)
    })
    show()
    expect(await screen.findByText('Chamado vinculado')).toBeInTheDocument()
    expect(screen.getByText('28180')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Atualizar' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Alterar status' })).not.toBeInTheDocument()
  })

  it('shows no ticket panel without a ticket key', async () => {
    serve(['conversation.read', 'contact.read', 'contact.classify', 'account.read'])
    show()
    expect(await screen.findByText('Berjon Brito')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Chamado' })).not.toBeInTheDocument()
    const asked = get.mock.calls.map((c) => String(c[0]))
    expect(asked.some((u) => u.endsWith('/ticket'))).toBe(false)
  })
})
