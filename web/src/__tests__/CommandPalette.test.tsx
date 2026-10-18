import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import { useLocation } from 'react-router-dom'
import CommandPalette from '../components/CommandPalette'
import { mockGets, renderAt, setSession, TENANT } from './testUtils'

vi.mock('axios')

const CONTACTS = `/api/v1/tenants/${TENANT}/contacts`
const CONVERSATIONS = `/api/v1/tenants/${TENANT}/inbox/conversations`
const CONTACT_ID = 'c1111111-1111-1111-1111-111111111111'
const CONV_ID = 'a1111111-1111-1111-1111-111111111111'

function Where() {
  const l = useLocation()
  return <div data-testid="where">{l.pathname + l.search}</div>
}

const palette = (onClose = vi.fn()) => (
  <>
    <CommandPalette open onClose={onClose} />
    <Where />
  </>
)

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  setSession()
  mockGets({
    [CONTACTS]: { items: [{ id: CONTACT_ID, display_name: 'Ana Souza', phone_e164: '+5511998887766' }], has_more: false, count: 1, limit: 5 },
    [CONVERSATIONS]: { items: [{ id: CONV_ID, contact_name: 'Ana Souza', contact_phone: '+5511998887766' }], has_more: false },
  })
})

describe('CommandPalette', () => {
  it('lists the screens without calling the server for a short query', async () => {
    renderAt(palette(), '/')
    expect(screen.getByRole('option', { name: /Contatos/ })).toBeInTheDocument()
    await userEvent.type(screen.getByRole('combobox'), 'a')
    expect(vi.mocked(axios.get)).not.toHaveBeenCalled()
    expect(screen.getByText(/ao menos 2 letras/)).toBeInTheDocument()
  })

  it('finds a screen by name without accents and opens it with Enter', async () => {
    const onClose = vi.fn()
    renderAt(palette(onClose), '/')
    await userEvent.type(screen.getByRole('combobox'), 'setores horarios{Enter}')
    expect(screen.getByTestId('where')).toHaveTextContent('/settings/attendance')
    expect(onClose).toHaveBeenCalled()
  })

  it('searches contacts and conversations of the company on the server and opens each', async () => {
    renderAt(palette(), '/')
    await userEvent.type(screen.getByRole('combobox'), 'ana souza')
    await waitFor(() => expect(screen.getAllByRole('option', { name: /Ana Souza/ })).toHaveLength(2))
    const calls = vi.mocked(axios.get).mock.calls
    expect(calls.some(([url, cfg]) => String(url).endsWith(CONTACTS) && (cfg as any).params.q === 'ana souza')).toBe(true)
    expect(calls.some(([url, cfg]) => String(url).endsWith(CONVERSATIONS) && (cfg as any).params.q === 'ana souza')).toBe(true)

    // the conversation is the last group: ArrowUp from the first row wraps to it
    await userEvent.keyboard('{ArrowUp}{Enter}')
    expect(screen.getByTestId('where')).toHaveTextContent(`/inbox?conversation_id=${CONV_ID}`)
  })

  it('opens a contact by click', async () => {
    renderAt(palette(), '/')
    await userEvent.type(screen.getByRole('combobox'), 'ana souza')
    const rows = await waitFor(() => {
      const r = screen.getAllByRole('option', { name: /Ana Souza/ })
      expect(r).toHaveLength(2)
      return r
    })
    await userEvent.click(rows[0])
    expect(screen.getByTestId('where')).toHaveTextContent(`/contacts/${CONTACT_ID}`)
  })

  it('says so when nothing matches, and when the server search fails', async () => {
    mockGets({})
    renderAt(palette(), '/')
    await userEvent.type(screen.getByRole('combobox'), 'zzzz')
    expect(await screen.findByText(/Não foi possível buscar/)).toBeInTheDocument()
    expect(screen.getByText(/Nada encontrado/)).toBeInTheDocument()
  })

  it('closes with Escape', async () => {
    const onClose = vi.fn()
    renderAt(palette(onClose), '/')
    await userEvent.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalled()
  })
})
