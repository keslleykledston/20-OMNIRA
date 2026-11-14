import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import ContextIntelligencePanel from '../components/inbox/ContextIntelligencePanel'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const CONV = 'conv-1'

const err = (status: number) => Promise.reject({ response: { status, data: '' } })

function mockView(data: any) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (String(url).endsWith(`/inbox/conversations/${CONV}/context-view`)) return { data }
    return err(404)
  })
}

beforeEach(() => {
  vi.resetAllMocks()
  setSession()
})

describe('ContextIntelligencePanel — CONTEXT.2C read model beside the conversation', () => {
  it('does not exist when the feature is off (404) or the actor cannot see it', async () => {
    vi.mocked(axios.get).mockImplementation(async () => err(404))
    renderAt(<ContextIntelligencePanel conversationId={CONV} />)
    await waitFor(() => expect(axios.get).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByLabelText('Inteligência de contexto')).not.toBeInTheDocument()
  })

  it('shows the none state without any topics or pendencies', async () => {
    mockView({
      analysis_state: 'none', stale: false, summary: '', coverage: { analyzed_message_count: 0, total_inbound_message_count: 0 },
      topics: [], pending_actions: [], suggestions: [], evidence_refs: [],
    })
    renderAt(<ContextIntelligencePanel conversationId={CONV} />)
    expect(await screen.findByText(/Ainda não analisado/)).toBeInTheDocument()
  })

  it('renders the summary, the stale badge, pending actions, suggestions and facts grouped by topic', async () => {
    mockView({
      analysis_state: 'ok', stale: true, summary: 'Cliente perguntou sobre o prazo de entrega.', updated_at: '2026-11-13T10:00:00Z',
      coverage: { analyzed_message_count: 3, total_inbound_message_count: 4 },
      topics: [
        {
          topic_id: 't-1', title: 'Entrega', status: 'open',
          facts: [
            { id: 'f-1', kind: 'need', text: 'saber previsão de entrega', truth_level: 'ai_inferred', status: 'open', evidence_message_ids: ['m-1'], created_at: '2026-11-13T09:59:00Z' },
            { id: 'f-2', kind: 'suggested_action', text: 'oferecer rastreio', truth_level: 'ai_inferred', status: 'open', evidence_message_ids: [], created_at: '2026-11-13T09:59:00Z' },
          ],
        },
      ],
      pending_actions: [{ id: 'f-1', kind: 'need', text: 'saber previsão de entrega', truth_level: 'ai_inferred', status: 'open', evidence_message_ids: ['m-1'], created_at: '2026-11-13T09:59:00Z' }],
      suggestions: [{ id: 'f-2', kind: 'suggested_action', text: 'oferecer rastreio', truth_level: 'ai_inferred', status: 'open', evidence_message_ids: [], created_at: '2026-11-13T09:59:00Z' }],
      evidence_refs: ['m-1'],
    })
    renderAt(<ContextIntelligencePanel conversationId={CONV} />)
    expect(await screen.findByText('Cliente perguntou sobre o prazo de entrega.')).toBeInTheDocument()
    expect(screen.getByText('Desatualizado')).toBeInTheDocument()
    expect(screen.getByText('Pendências identificadas (1)')).toBeInTheDocument()
    expect(screen.getByText('Sugestões (1)')).toBeInTheDocument()
    expect(screen.getByText('Entrega')).toBeInTheDocument()
    // each fact appears twice: once in its flat pending/suggestions list, once under "Por assunto"
    expect(screen.getAllByText('saber previsão de entrega').length).toBeGreaterThan(0)
    expect(screen.getAllByText('oferecer rastreio').length).toBeGreaterThan(0)
  })

  it('shows the needs_customer_account_selection notice only when the server includes it', async () => {
    mockView({
      analysis_state: 'ok', stale: false, summary: 'resumo', coverage: { analyzed_message_count: 1, total_inbound_message_count: 1 },
      topics: [], pending_actions: [], suggestions: [], evidence_refs: [], needs_customer_account_selection: true,
    })
    renderAt(<ContextIntelligencePanel conversationId={CONV} />)
    expect(await screen.findByText(/mais de uma empresa/)).toBeInTheDocument()
  })

  it('requests a refresh via POST and refetches on success', async () => {
    mockView({ analysis_state: 'none', stale: false, summary: '', coverage: { analyzed_message_count: 0, total_inbound_message_count: 0 }, topics: [], pending_actions: [], suggestions: [], evidence_refs: [] })
    vi.mocked(axios.post).mockResolvedValue({ data: { forced: true } })
    renderAt(<ContextIntelligencePanel conversationId={CONV} />)
    await screen.findByText(/Ainda não analisado/)
    await userEvent.click(screen.getByText('Atualizar'))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    const [url] = vi.mocked(axios.post).mock.calls[0] as any[]
    expect(String(url)).toContain(`/inbox/conversations/${CONV}/context-analysis/refresh`)
  })

  it('shows a throttled message on 429 without breaking the panel', async () => {
    mockView({ analysis_state: 'none', stale: false, summary: '', coverage: { analyzed_message_count: 0, total_inbound_message_count: 0 }, topics: [], pending_actions: [], suggestions: [], evidence_refs: [] })
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 429 } })
    renderAt(<ContextIntelligencePanel conversationId={CONV} />)
    await screen.findByText(/Ainda não analisado/)
    await userEvent.click(screen.getByText('Atualizar'))
    expect(await screen.findByText(/solicitada há pouco/)).toBeInTheDocument()
  })
})
