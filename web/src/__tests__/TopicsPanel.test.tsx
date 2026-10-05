import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import TopicsPanel from '../components/topics/TopicsPanel'
import ContactTopics from '../components/topics/ContactTopics'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const CONV = 'conv-1'
const T1 = 't-1'
const T2 = 't-2'

const topics = [
  { id: T1, title: 'Pedido 837', status: 'open', privacy_policy: 'public', source: 'rule', last_activity_at: '2026-10-04T12:00:00Z', message_count: 3, ticket_count: 1 },
  { id: T2, title: 'Nota fiscal 992', status: 'resolved', privacy_policy: 'public', source: 'rule', last_activity_at: '2026-10-04T11:00:00Z', message_count: 1, ticket_count: 0 },
]

type Handler = (url: string, body?: any) => any
interface Fixture {
  access?: string[]
  get?: Record<string, any | ((url: string) => any)>
  post?: Record<string, Handler>
  patch?: Record<string, Handler>
}

const err = (status: number) => Promise.reject({ response: { status, data: '' } })

function install(f: Fixture) {
  const get = {
    '/me/access': { permissions: f.access ?? ['topic.read', 'topic.manage'] },
    [`/inbox/conversations/${CONV}/topics`]: { items: topics },
    [`/inbox/conversations/${CONV}/ambiguities`]: { items: [] },
    ...(f.get ?? {}),
  } as Record<string, any>
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    const key = Object.keys(get).find((k) => url.endsWith(k))
    if (!key) return err(404)
    const v = get[key]
    return typeof v === 'function' ? v(url) : { data: v }
  })
  const route = (table?: Record<string, Handler>) => async (url: string, body?: any) => {
    const key = Object.keys(table ?? {}).find((k) => url.endsWith(k))
    if (!key) return err(404)
    const out = table![key](url, body)
    return out instanceof Promise ? out : { data: out }
  }
  vi.mocked(axios.post).mockImplementation(route(f.post) as any)
  vi.mocked(axios.patch).mockImplementation(route(f.patch) as any)
}

const posts = () => vi.mocked(axios.post).mock.calls.map(([u, b]) => ({ url: String(u), body: b as any }))

beforeEach(() => {
  vi.resetAllMocks()
  setSession()
})

const openTopic = async (title: string) => {
  await userEvent.click(await screen.findByRole('button', { name: new RegExp(title) }))
}

describe('TopicsPanel — the topic surface beside the conversation', () => {
  it('does not exist when topics are switched off (404), and the conversation is untouched', async () => {
    vi.mocked(axios.get).mockImplementation(async () => err(404))
    renderAt(<TopicsPanel conversationId={CONV} />)
    await waitFor(() => expect(axios.get).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 30))
    expect(screen.queryByLabelText('Assuntos da conversa')).not.toBeInTheDocument()
  })

  it('lists the subjects with counts and opens one on click; a second click closes it', async () => {
    install({})
    renderAt(<TopicsPanel conversationId={CONV} />)
    const list = await screen.findByRole('list', { name: 'Lista de assuntos' })
    expect(within(list).getByText('Pedido 837')).toBeInTheDocument()
    expect(within(list).getByText('3 mensagens')).toBeInTheDocument()
    expect(within(list).getByText('1 mensagem')).toBeInTheDocument()
    expect(within(list).getByText(/1 chamado/)).toBeInTheDocument()
    expect(within(list).getByText('Resolvido')).toBeInTheDocument()
    expect(screen.queryByLabelText(/Detalhes do assunto/)).not.toBeInTheDocument()
    await openTopic('Pedido 837')
    expect(screen.getByLabelText('Detalhes do assunto Pedido 837')).toBeInTheDocument()
    await openTopic('Pedido 837')
    expect(screen.queryByLabelText(/Detalhes do assunto/)).not.toBeInTheDocument()
  })

  it('shows an empty state and lets a manager create a subject', async () => {
    install({
      get: { [`/inbox/conversations/${CONV}/topics`]: { items: [] } },
      post: { [`/inbox/conversations/${CONV}/topics`]: (_u, b) => ({ id: 'new-1', title: b.title, status: 'open', last_activity_at: '2026-10-04T12:00:00Z' }) },
    })
    renderAt(<TopicsPanel conversationId={CONV} />)
    expect(await screen.findByText(/Nenhum assunto ainda/)).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Título do novo assunto'), 'Troca de plano')
    await userEvent.click(screen.getByRole('button', { name: 'Criar' }))
    await waitFor(() => expect(posts().some((p) => p.url.endsWith(`/inbox/conversations/${CONV}/topics`) && p.body.title === 'Troca de plano')).toBe(true))
  })

  it('offers no write action to someone without topic.manage', async () => {
    install({ access: ['topic.read'] })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await screen.findByRole('list', { name: 'Lista de assuntos' })
    expect(screen.queryByLabelText('Título do novo assunto')).not.toBeInTheDocument()
    await openTopic('Pedido 837')
    expect(screen.queryByRole('button', { name: 'Resolver' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Sugerir resposta/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Gerar convite/ })).not.toBeInTheDocument()
  })

  it('asks a person to decide an ambiguous message and posts the chosen subject', async () => {
    install({
      get: {
        [`/inbox/conversations/${CONV}/ambiguities`]: { items: [{ id: 'amb-1', message_id: 'm-1', kind: 'conversation', created_at: '2026-10-04T12:00:00Z', candidates: [{ topic_id: T1, title: 'Pedido 837', score: 0.62 }, { topic_id: T2, score: 0.6 }] }] },
      },
      post: { '/ambiguities/amb-1/resolve': () => ({}) },
    })
    renderAt(<TopicsPanel conversationId={CONV} />)
    const region = await screen.findByRole('region', { name: 'Aguardando decisão' })
    await userEvent.click(within(region).getByRole('button', { name: 'Pedido 837' }))
    await waitFor(() => expect(posts()).toContainEqual({ url: expect.stringContaining('/ambiguities/amb-1/resolve'), body: { topic_id: T1 } }))
    // a candidate without a title falls back to the title the panel already knows
    expect(within(region).getByRole('button', { name: 'Nota fiscal 992' })).toBeInTheDocument()
  })

  it('opens a new subject for an ambiguous message using the typed title', async () => {
    install({
      get: { [`/inbox/conversations/${CONV}/ambiguities`]: { items: [{ id: 'amb-2', message_id: 'm-2', kind: 'conversation', created_at: '2026-10-04T12:00:00Z', candidates: [] }] } },
      post: { '/ambiguities/amb-2/resolve': () => ({}) },
    })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await userEvent.type(await screen.findByLabelText('Título do novo assunto'), 'Acesso bloqueado')
    await userEvent.click(within(screen.getByRole('region', { name: 'Aguardando decisão' })).getByRole('button', { name: 'Novo assunto' }))
    await waitFor(() => expect(posts()).toContainEqual({ url: expect.stringContaining('/ambiguities/amb-2/resolve'), body: { new_topic_title: 'Acesso bloqueado' } }))
  })

  it('shows the logical timeline oldest first, marks shared messages, and says nothing is lost', async () => {
    install({
      get: {
        [`/topics/${T1}/messages?limit=20`]: {
          items: [
            { id: 'm2', direction: 'outbound', message_type: 'text', body: 'Estamos verificando', created_at: '2026-10-04T12:05:00Z', relation: 'primary', decision_source: 'agent' },
            { id: 'm1', direction: 'inbound', message_type: 'text', body: 'meu pedido 837 não chegou', created_at: '2026-10-04T12:00:00Z', relation: 'secondary', decision_source: 'entity' },
          ],
        },
      },
    })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    const region = await screen.findByLabelText('Linha do tempo do assunto')
    const texts = (await within(region).findAllByText(/pedido 837 não chegou|Estamos verificando/)).map((n) => n.textContent)
    expect(texts[0]).toContain('pedido 837 não chegou')
    expect(texts[1]).toContain('Estamos verificando')
    expect(screen.getByText('também aqui')).toBeInTheDocument()
    expect(screen.getByText(/mesmo pedido\/documento/)).toBeInTheDocument()
  })
})

describe('TopicsPanel — versioned summary', () => {
  const summaries = (...items: any[]) => ({ [`/topics/${T1}/summaries`]: { items } })
  const v = (n: number, status: string, text: string) => ({ id: `s${n}`, version: n, status, summary_text: text, authored_by: status === 'corrected' ? 'agent' : 'machine', created_at: '2026-10-04T12:00:00Z' })

  it('labels a machine draft as such and confirms it with one click', async () => {
    install({ get: summaries(v(1, 'ai_inferred', 'Pedido atrasado')), post: { [`/topics/${T1}/summary/confirm`]: () => v(1, 'agent_confirmed', 'Pedido atrasado') } })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    expect(await screen.findByText('Rascunho da IA')).toBeInTheDocument()
    expect(screen.getByText(/Confira antes de confiar/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Confirmar resumo' }))
    await waitFor(() => expect(posts().some((p) => p.url.endsWith(`/topics/${T1}/summary/confirm`))).toBe(true))
  })

  it('corrects a summary as a NEW version and keeps the history readable', async () => {
    install({
      get: summaries(v(3, 'corrected', 'O cliente quer trocar o produto.'), v(2, 'ai_inferred', 'Talvez cancelamento'), v(1, 'superseded', 'Versão antiga')),
      post: { [`/topics/${T1}/summary/correct`]: () => v(4, 'corrected', 'x') },
    })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    expect(await screen.findByText('Corrigido pelo atendente')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Confirmar resumo' })).not.toBeInTheDocument() // only a machine draft is confirmed
    await userEvent.click(screen.getByRole('button', { name: /Ver histórico \(3 versões\)/ }))
    const hist = screen.getByRole('list', { name: 'Histórico de versões' })
    expect(within(hist).getByText('Talvez cancelamento')).toBeInTheDocument()
    expect(within(hist).getByText('Versão antiga')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Corrigir' }))
    const box = screen.getByLabelText('Texto corrigido do resumo')
    await userEvent.clear(box)
    await userEvent.type(box, 'Troca, não cancelamento.')
    await userEvent.click(screen.getByRole('button', { name: 'Salvar como nova versão' }))
    await waitFor(() => expect(posts()).toContainEqual({ url: expect.stringContaining(`/topics/${T1}/summary/correct`), body: { summary_text: 'Troca, não cancelamento.' } }))
  })

  it('says so when the summary provider is unavailable, instead of failing silently', async () => {
    install({ get: summaries(), post: { [`/topics/${T1}/summary/generate`]: () => err(503) } })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    await userEvent.click(await screen.findByRole('button', { name: 'Gerar resumo' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/indisponível/i)
  })
})

describe('TopicsPanel — tickets, copilot and private handoff', () => {
  it('shows the ticket policy advice and applies only an action the backend allows', async () => {
    install({
      get: {
        [`/topics/${T1}/tickets`]: { items: [] },
        [`/topics/${T1}/ticket-policy`]: { action: 'adopt_active', reason: 'O chamado ativo da conversa ainda não pertence a nenhum assunto.', allowed_actions: ['adopt_active', 'share_active'] },
      },
      post: { [`/topics/${T1}/ticket-policy/apply`]: () => ({ ticket_id: 'tk', relation: 'primary', created: false }) },
    })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    expect(await screen.findByText(/ainda não pertence a nenhum assunto/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Abrir chamado/ })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Vincular ao chamado ativo' }))
    await waitFor(() => expect(posts()).toContainEqual({ url: expect.stringContaining(`/topics/${T1}/ticket-policy/apply`), body: { action: 'adopt_active' } }))
  })

  it('offers no action when the policy needs a person (a second concurrent ticket is never one click away)', async () => {
    install({
      get: {
        [`/topics/${T1}/tickets`]: { items: [{ id: 'tk1', status: 'open', priority: 'medium', subject: 'Atraso', relation: 'primary' }] },
        [`/topics/${T1}/ticket-policy`]: { action: 'needs_agent', reason: 'O chamado ativo já pertence a outro assunto.', allowed_actions: ['share_active'] },
      },
    })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    expect(await screen.findByText('Atraso')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Abrir chamado/ })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Relacionar ao chamado ativo' })).toBeInTheDocument()
  })

  it('shows a copilot draft with OMNIRA warnings, labelled as NOT sent, and never posts a message', async () => {
    install({
      post: { [`/topics/${T1}/copilot/suggest-reply`]: () => ({ reply: 'Já cancelei o pedido. Veja https://x.example', missing_info: ['endereço de entrega'], needs_human: true, warnings: ['claims_action_done', 'link_not_in_context'], sent: false }) },
    })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    await userEvent.click(await screen.findByRole('button', { name: 'Sugerir resposta' }))
    expect(await screen.findByTestId('copilot-draft')).toHaveTextContent('Já cancelei o pedido')
    expect(screen.getByText(/não foi enviado/i)).toBeInTheDocument()
    expect(screen.getByText(/Afirma que algo já foi feito/)).toBeInTheDocument()
    expect(screen.getByText(/link que não está na conversa/)).toBeInTheDocument()
    expect(screen.getByText('Pede decisão de uma pessoa')).toBeInTheDocument()
    expect(screen.getByText('endereço de entrega')).toBeInTheDocument()
    // the only POST ever made is the suggestion itself: nothing reaches the message endpoints
    expect(posts().map((p) => p.url).filter((u) => /\/messages|\/send/.test(u))).toEqual([])
  })

  it('removes the copilot when it is switched off (404)', async () => {
    install({ post: { [`/topics/${T1}/copilot/suggest-reply`]: () => err(404) } })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    await userEvent.click(await screen.findByRole('button', { name: 'Sugerir resposta' }))
    await waitFor(() => expect(screen.queryByLabelText('Copiloto de resposta')).not.toBeInTheDocument())
  })

  it('reports an unavailable copilot instead of failing silently', async () => {
    install({ post: { [`/topics/${T1}/copilot/suggest-reply`]: () => err(503) } })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    await userEvent.click(await screen.findByRole('button', { name: 'Sugerir resposta' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/indisponível/i)
  })

  it('shows the private-chat code once and forgets it when the subject changes', async () => {
    install({
      get: { [`/inbox/conversations/${CONV}/topics`]: { items: [topics[0], { ...topics[1], status: 'open' }] } },
      post: { [`/topics/${T1}/handoffs`]: () => ({ handoff: { id: 'h1', status: 'pending', expires_at: '2026-10-05T12:00:00Z' }, token: 'omn-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', instructions: 'Envie esta mensagem no chat privado.' }) },
    })
    renderAt(<TopicsPanel conversationId={CONV} />)
    await openTopic('Pedido 837')
    await userEvent.click(await screen.findByRole('button', { name: 'Gerar convite' }))
    expect(await screen.findByTestId('handoff-token')).toHaveTextContent(/^omn-A+$/)
    expect(screen.getByText(/aparece só agora e vale uma única vez/)).toBeInTheDocument()
    // nothing is stored in the browser
    expect(JSON.stringify({ ...localStorage })).not.toContain('omn-')
    expect(JSON.stringify({ ...sessionStorage })).not.toContain('omn-')
    await openTopic('Nota fiscal 992')
    expect(screen.queryByTestId('handoff-token')).not.toBeInTheDocument()
  })

  it('resets everything when the conversation changes', async () => {
    install({})
    function Switcher() {
      const [c, setC] = useState(CONV)
      return (
        <>
          <button onClick={() => setC('conv-2')}>trocar conversa</button>
          <TopicsPanel conversationId={c} />
        </>
      )
    }
    renderAt(<Switcher />)
    await openTopic('Pedido 837')
    expect(screen.getByLabelText('Detalhes do assunto Pedido 837')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'trocar conversa' }))
    await waitFor(() => expect(screen.queryByLabelText(/Detalhes do assunto/)).not.toBeInTheDocument())
  })
})

describe('ContactTopics', () => {
  it("lists the contact's subjects", async () => {
    vi.mocked(axios.get).mockImplementation(async (url: string) => (url.endsWith('/contacts/c-1/topics') ? { data: { items: topics } } : err(404)))
    renderAt(<ContactTopics contactId="c-1" />)
    expect(await screen.findByText('Pedido 837')).toBeInTheDocument()
    expect(screen.getByText('Nota fiscal 992')).toBeInTheDocument()
  })

  it('does not exist when topics are off', async () => {
    vi.mocked(axios.get).mockImplementation(async () => err(404))
    renderAt(<ContactTopics contactId="c-2" />)
    await waitFor(() => expect(axios.get).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 30))
    expect(screen.queryByLabelText('Assuntos do contato')).not.toBeInTheDocument()
  })
})
