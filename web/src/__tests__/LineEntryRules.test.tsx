import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import axios from 'axios'
import { LineEntryRules } from '../components/people/LineEntryRules'

vi.mock('axios')
vi.mock('../lib/session', async (orig) => ({ ...(await orig<typeof import('../lib/session')>()), getTenantId: () => 'tenant-a', authHeaders: () => ({}) }))

const groups = [
  { id: 'g-triagem', name: 'Triagem', isDefault: true },
  { id: 'g-fin', name: 'Financeiro', isDefault: false },
]
const lines = { items: [{ id: 'line-1', provider: 'waha', provider_kind: 'unofficial', label: 'WhatsApp +55 92 9000-0001', status: 'active', can_send_text: true, window_required: false }] }

function mount(canManage = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><LineEntryRules groups={groups} canManage={canManage} /></QueryClientProvider>)
}

describe('LineEntryRules (ADR-0043, entrada por canal)', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/channels/lines')) return { data: lines }
      if (url.endsWith('/queues/lines')) return { data: { items: [{ connection_id: 'line-1', queue_id: 'g-fin' }] } }
      throw new Error('unexpected GET ' + url)
    })
  })

  it('shows each line with the group it feeds, defaulting to the default group when none is set', async () => {
    mount()
    const select = await screen.findByRole('combobox', { name: /WhatsApp \+55 92 9000-0001/ })
    await waitFor(() => expect((select as HTMLSelectElement).value).toBe('g-fin'))
    expect(screen.getByRole('option', { name: 'Fila padrão (Triagem)' })).toBeTruthy()
  })

  it('sets the rule with PUT and clears it with null', async () => {
    vi.mocked(axios.put).mockResolvedValue({ data: { connection_id: 'line-1', queue_id: null } })
    mount()
    const select = await screen.findByRole('combobox')
    await waitFor(() => expect((select as HTMLSelectElement).value).toBe('g-fin'))
    await userEvent.selectOptions(select, '')
    await waitFor(() => expect(axios.put).toHaveBeenCalled())
    const [url, body] = vi.mocked(axios.put).mock.calls[0]
    expect(url).toMatch(/\/tenants\/tenant-a\/channels\/lines\/line-1\/queue$/)
    expect(body).toEqual({ queue_id: null })
  })

  it('is read-only without permission and says why a refusal happened', async () => {
    mount(false)
    expect(((await screen.findByRole('combobox')) as HTMLSelectElement).disabled).toBe(true)
  })

  it('shows a readable error when the server refuses', async () => {
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 404, data: 'queue not found' } })
    mount()
    const select = await screen.findByRole('combobox')
    await waitFor(() => expect((select as HTMLSelectElement).value).toBe('g-fin'))
    await userEvent.selectOptions(select, 'g-triagem')
    expect((await screen.findByRole('alert')).textContent).toMatch(/não existe mais/)
  })
})
