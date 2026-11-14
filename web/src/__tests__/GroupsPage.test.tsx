import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import GroupsPage from '../pages/GroupsPage'
import { authorLabel, groupPreview, messageText, sortGroupMessages } from '../lib/groupModel'
import type { AccountGroupsSummary, AvailableGroup, Group, GroupMessage, GroupParticipant } from '../lib/groups'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const READ = ['group.read']
const MANAGE = ['group.read', 'group.manage']

const group = (over: Partial<Group> = {}): Group => ({
  id: 'g-1',
  name: '0 - K3G Solutions Oficial',
  enabled: true,
  purpose: 'other',
  account_id: null,
  account_name: null,
  additional_accounts: [],
  connection_id: 'c-1',
  connection_account: '559200000000',
  ai_observer_enabled: false,
  is_alert_channel: false,
  last_message_at: new Date().toISOString(),
  last_message: { author_name: 'Rafael', from_me: false, message_type: 'text', preview: 'certo' },
  ...over,
})
const msg = (id: string, minAgo: number, over: Partial<GroupMessage> = {}): GroupMessage => ({
  id,
  author_name: 'Rafael',
  from_me: false,
  message_type: 'text',
  body: `texto ${id}`,
  sent_at: new Date(Date.now() - minAgo * 60_000).toISOString(),
  suggestion: null,
  participant_id: null,
  company_name: null,
  company_role: null,
  is_downstream_client: null,
  image_caption: null,
  ...over,
})

interface Server {
  permissions: string[]
  groups?: Group[]
  messages?: GroupMessage[] // newest-first thread, served in pages of `pageSize`
  pageSize?: number
  available?: AvailableGroup[]
  availableTotal?: number
  hubs?: Array<{ id: string; name: string; role: string; can_manage_groups?: boolean }>
  accountsSummary?: AccountGroupsSummary[]
  crmCompanies?: Array<{ id: string; name: string; cnpj?: string }>
  localAccounts?: Array<{ id: string; name: string }>
  participants?: GroupParticipant[]
}

function serve(s: Server) {
  const pageSize = s.pageSize ?? 100
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: s.permissions } }
    if (url.endsWith('/groups/accounts-summary')) return { data: { items: s.accountsSummary ?? [] } }
    if (url.endsWith('/crm/companies')) return { data: { items: s.crmCompanies ?? [] } }
    if (url.endsWith('/accounts')) return { data: { items: s.localAccounts ?? [] } }
    if (url.endsWith('/groups/available')) {
      const items = (s.available ?? []).filter((g) => !config?.params?.q || g.name.toLowerCase().includes(String(config.params.q).toLowerCase()))
      return { data: { items, count: items.length, total: s.availableTotal ?? items.length } }
    }
    if (/\/groups\/[^/]+\/messages$/.test(url)) {
      const all = [...(s.messages ?? [])].sort((a, b) => b.sent_at.localeCompare(a.sent_at))
      const offset = Number(config?.params?.cursor || 0)
      const items = all.slice(offset, offset + pageSize)
      const more = offset + pageSize < all.length
      return { data: { items, has_more: more, next_cursor: more ? String(offset + pageSize) : undefined } }
    }
    if (/\/groups\/[^/]+\/participants$/.test(url)) return { data: { items: s.participants ?? [] } }
    if (url.endsWith('/groups')) return { data: { items: s.groups ?? [] } }
    if (url.endsWith('/hubs')) return { data: { items: s.hubs ?? [] } }
    return Promise.reject({ response: { status: 404 } })
  })
}

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('groupModel', () => {
  it('shows the text, or the kind of media when there is none, and never invents content', () => {
    expect(messageText({ body: ' oi ', message_type: 'text' })).toBe('oi')
    expect(messageText({ body: '', message_type: 'image' })).toBe('[Foto]')
    expect(messageText({ body: '', message_type: 'audio' })).toBe('[Áudio]')
    expect(messageText({ body: '', message_type: 'text' })).toBe('')
  })
  it('labels the author: "Você" for our own, a neutral word when the name is unknown', () => {
    expect(authorLabel({ author_name: 'Rafael', from_me: false })).toBe('Rafael')
    expect(authorLabel({ author_name: '', from_me: false })).toBe('Participante')
    expect(authorLabel({ author_name: 'Rafael', from_me: true })).toBe('Você')
  })
  it('builds the list preview as "Autor: texto"', () => {
    expect(groupPreview(group())).toBe('Rafael: certo')
    expect(groupPreview(group({ last_message: { author_name: '', from_me: true, message_type: 'image', preview: '' } }))).toBe('Você: [Foto]')
    expect(groupPreview(group({ last_message: null }))).toBe('Sem mensagens')
  })
  it('orders oldest first without duplicates', () => {
    const out = sortGroupMessages([msg('c', 1), msg('a', 30), msg('b', 10), msg('b', 10)])
    expect(out.map((m) => m.id)).toEqual(['a', 'b', 'c'])
  })
})

describe('GroupsPage', () => {
  it('refuses the page without group.read and never asks for groups', async () => {
    serve({ permissions: [] })
    renderAt(<GroupsPage />)
    expect(await screen.findByText(/não tem permissão para ver os grupos/i)).toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => (u as string).endsWith('/groups'))).toBe(false)
  })

  it('tells an administrator how to start, and a reader that an administrator chooses', async () => {
    serve({ permissions: MANAGE, groups: [] })
    const { unmount } = renderAt(<GroupsPage />)
    expect(await screen.findByText('Nenhum grupo habilitado')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Gerenciar grupos' })).toBeInTheDocument()
    expect(await screen.findByText(/Escolha em "Gerenciar grupos"/)).toBeInTheDocument()
    expect(screen.queryByText(/Um administrador escolhe/)).not.toBeInTheDocument()
    unmount()

    serve({ permissions: READ, groups: [] })
    renderAt(<GroupsPage />)
    expect(await screen.findByText(/Um administrador escolhe/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Gerenciar grupos' })).not.toBeInTheDocument()
  })

  it('offers "Organizar no Hub" only to an operator the server reports as a Hub agent with the groups scope, and it navigates away', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [], hubs: [{ id: 'hub-1', name: 'K3G', role: 'hub_agent', can_manage_groups: true }] })
    renderAt(<GroupsPage />, '/grupos', '/grupos')
    const link = await screen.findByRole('button', { name: 'Organizar no Hub' })
    await user.click(link)
    expect(await screen.findByTestId('elsewhere')).toBeInTheDocument()
  })

  it('hides "Organizar no Hub" for a tenant operator with no Hub groups access', async () => {
    serve({ permissions: MANAGE, groups: [], hubs: [] })
    renderAt(<GroupsPage />)
    await screen.findByRole('button', { name: 'Gerenciar grupos' })
    expect(screen.queryByRole('button', { name: 'Organizar no Hub' })).not.toBeInTheDocument()
  })

  it('opens a group read-only: oldest first, author on each run, own messages as Você, media named, no composer', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ,
      groups: [group()],
      messages: [
        msg('m3', 1, { author_name: '', from_me: true, body: 'ok' }),
        msg('m2', 5, { author_name: 'Felipe', body: '', message_type: 'image' }),
        msg('m1', 10, { author_name: 'Rafael', body: 'primeira' }),
      ],
    })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('primeira')
    // An empty-body image message shows the actual photo (ADR-0015 addendum 2), never a "[Foto]" placeholder.
    const texts = [screen.getByText('primeira'), screen.getByAltText('Imagem do grupo'), screen.getByText('ok')]
    // document order = oldest -> newest
    expect(texts[0].compareDocumentPosition(texts[1]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(texts[1].compareDocumentPosition(texts[2]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.getAllByText('Felipe').length).toBeGreaterThan(0)
    expect(screen.getByText(/Somente leitura/)).toBeInTheDocument()
    expect(screen.getByText(/não são enviadas pelo OMNIRA/)).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Apagar histórico' })).not.toBeInTheDocument() // reader only
  })

  it('loads older messages above the ones shown, in order', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ,
      groups: [group()],
      pageSize: 2,
      messages: [msg('m1', 40), msg('m2', 30), msg('m3', 20), msg('m4', 10)],
    })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('texto m4')
    expect(screen.queryByText('texto m1')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Carregar mensagens anteriores' }))
    await screen.findByText('texto m1')
    const order = screen.getAllByText(/^texto m/).map((n) => n.textContent)
    expect(order).toEqual(['texto m1', 'texto m2', 'texto m3', 'texto m4'])
  })

  it('lets an administrator delete a group\'s history only after confirming', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [group()], messages: [msg('m1', 5)] })
    vi.mocked(axios.delete).mockResolvedValue({ data: { deleted: 1 } })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('texto m1')
    await user.click(screen.getByRole('button', { name: 'Apagar histórico' }))
    await screen.findByText('Apagar o histórico deste grupo?')
    // honest about backups: the messages can still be inside backups already taken
    expect(screen.getByText(/Cópias de segurança já feitas ainda podem conter essas mensagens/)).toBeInTheDocument()
    expect(screen.getByText(/35 no disco externo/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancelar' }))
    expect(axios.delete).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Apagar histórico' }))
    const dialog = await screen.findByText('Apagar o histórico deste grupo?')
    await user.click(within(dialog.closest('[role="dialog"]') as HTMLElement).getByRole('button', { name: 'Apagar histórico' }))
    await waitFor(() => expect(axios.delete).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.delete).mock.calls[0][0]).toMatch(/\/groups\/g-1\/messages$/)
  })

  it('"Canal de alerta" fica desabilitado num grupo que não é Interno · Gestão/Equipe (ADR-0058 addendum)', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [group({ purpose: 'other' })], messages: [] })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    expect(await screen.findByRole('checkbox', { name: 'Canal de alerta' })).toBeDisabled()
  })

  it('"Canal de alerta" fica habilitado num grupo Interno · Gestão/Equipe (ADR-0058 addendum)', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [group({ purpose: 'internal_team' })], messages: [] })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    expect(await screen.findByRole('checkbox', { name: 'Canal de alerta' })).toBeEnabled()
  })
})

describe('GroupsPage — imagem no chat do grupo (ADR-0015 addendum 2)', () => {
  it('mostra a imagem com a URL correta e a legenda que a IA já leu', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ,
      groups: [group()],
      messages: [msg('m1', 1, { message_type: 'image', body: '', image_caption: 'um roteador com a luz vermelha piscando' })],
    })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    const img = await screen.findByAltText('Imagem do grupo')
    expect(img).toHaveAttribute('src', expect.stringContaining('/tenants/tenant-a-uuid/groups/g-1/messages/m1/media'))
    expect(screen.getByText('um roteador com a luz vermelha piscando')).toBeInTheDocument()
  })

  it('sem legenda ainda (análise pendente), mostra só a imagem', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ, groups: [group()], messages: [msg('m1', 1, { message_type: 'image', body: '' })] })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByAltText('Imagem do grupo')
    expect(screen.queryByText('O que a IA leu na imagem')).not.toBeInTheDocument()
  })

  it('se a imagem falhar ao carregar, mostra um aviso em vez de quebrar', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ, groups: [group()], messages: [msg('m1', 1, { message_type: 'image', body: '' })] })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    const img = await screen.findByAltText('Imagem do grupo')
    fireEvent.error(img)
    expect(await screen.findByText('Não foi possível carregar esta imagem.')).toBeInTheDocument()
    expect(screen.queryByAltText('Imagem do grupo')).not.toBeInTheDocument()
  })
})

describe('GroupsPage — identificar participante (ADR-0015 addendum: alias/empresa/função, sem telefone)', () => {
  it('mostra empresa · função ao lado do autor quando já vinculado, e permite editar o alias', async () => {
    const user = userEvent.setup()
    serve({
      permissions: MANAGE,
      groups: [group()],
      messages: [msg('m1', 5, { author_name: 'Felipe', participant_id: 'p-1', company_name: 'K3G Solutions', company_role: 'technical_contact' })],
      participants: [{ id: 'p-1', display_name: 'Felipe', alias: null, company_account_id: 'acc-9', company_name: 'K3G Solutions', company_role: 'technical_contact', is_downstream_client: false, message_count: 1, last_seen_at: new Date().toISOString() }],
    })
    vi.mocked(axios.put).mockResolvedValue({ data: {} })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('Felipe')
    expect(screen.getByText('· K3G Solutions · Contato técnico')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Identificar participante' }))
    const alias = await screen.findByLabelText('Nome aparente')
    await user.type(alias, 'Felipe (Suporte K3G)')
    await user.click(screen.getByRole('button', { name: 'Salvar nome aparente' }))
    await waitFor(() =>
      expect(axios.put).toHaveBeenCalledWith(expect.stringContaining('/groups/participants/p-1/details'), { alias: 'Felipe (Suporte K3G)' }, expect.anything()),
    )
  })

  // ADR-0015 addendum 3: a empresa vinculada pode ser cliente do Cliente (ex.: a Uplink, cliente da 4WNET).
  it('marca a empresa vinculada como "Cliente" (cliente do Cliente) quando o checkbox é marcado', async () => {
    const user = userEvent.setup()
    serve({
      permissions: MANAGE,
      groups: [group()],
      messages: [msg('m1', 5, { author_name: 'Contato', participant_id: 'p-2' })],
      participants: [
        { id: 'p-2', display_name: 'Contato', alias: null, company_account_id: null, company_name: null, company_role: null, is_downstream_client: false, message_count: 1, last_seen_at: new Date().toISOString() },
      ],
      localAccounts: [{ id: 'acc-up', name: 'Uplink' }],
    })
    vi.mocked(axios.put).mockResolvedValue({ data: {} })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('Contato')
    await user.click(screen.getByRole('button', { name: 'Identificar participante' }))
    // Espera os dados do participante assentarem antes de interagir — o efeito que inicializa alias/role/
    // isDownstreamClient a partir da query reseta o checkbox se ela resolver depois do clique.
    await waitFor(() => expect(screen.getByLabelText('Nome aparente')).toHaveAttribute('placeholder', 'Contato'))
    await user.click(await screen.findByRole('checkbox', { name: /Cliente/ }))
    await user.click(await screen.findByRole('button', { name: /Uplink/ }))
    await user.click(screen.getByRole('button', { name: 'Vincular empresa' }))
    await waitFor(() =>
      expect(axios.put).toHaveBeenCalledWith(
        expect.stringContaining('/groups/participants/p-2/company/link'),
        { account_id: 'acc-up', role: 'other', is_downstream_client: true },
        expect.anything(),
      ),
    )
  })

  // O vínculo pode ser editado no lugar (trocar a função / marcar-desmarcar "Cliente") sem precisar
  // remover e refazer — a empresa já selecionada continua selecionada ao abrir o modal.
  it('edita a função de um vínculo já existente sem precisar remover antes', async () => {
    const user = userEvent.setup()
    serve({
      permissions: MANAGE,
      groups: [group()],
      messages: [msg('m1', 5, { author_name: 'Felipe', participant_id: 'p-1', company_name: 'K3G Solutions', company_role: 'technical_contact' })],
      participants: [
        { id: 'p-1', display_name: 'Felipe', alias: null, company_account_id: 'acc-9', company_name: 'K3G Solutions', company_role: 'technical_contact', is_downstream_client: false, message_count: 1, last_seen_at: new Date().toISOString() },
      ],
    })
    vi.mocked(axios.put).mockResolvedValue({ data: {} })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('Felipe')
    await user.click(screen.getByRole('button', { name: 'Identificar participante' }))
    await waitFor(() => expect(screen.getByLabelText('Nome aparente')).toHaveAttribute('placeholder', 'Felipe'))

    await user.selectOptions(screen.getByRole('combobox', { name: 'Função' }), 'owner')
    await user.click(screen.getByRole('button', { name: 'Editar função/Cliente desta empresa' }))
    await waitFor(() =>
      expect(axios.put).toHaveBeenCalledWith(
        expect.stringContaining('/groups/participants/p-1/company/link'),
        { account_id: 'acc-9', role: 'owner', is_downstream_client: false },
        expect.anything(),
      ),
    )
    // O modal continua aberto — editar não deve fechá-lo, só "Fechar"/X (bug relatado pelo usuário).
    expect(await screen.findByText('Empresa salva.')).toBeInTheDocument()
    expect(screen.getByRole('dialog', { name: 'Identificar participante' })).toBeInTheDocument()
    // "Remover" nunca foi chamado — a edição não passou por lá.
    expect(vi.mocked(axios.delete)).not.toHaveBeenCalled()
  })

  it('não oferece "Identificar participante" sem group.manage', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ,
      groups: [group()],
      messages: [msg('m1', 5, { author_name: 'Felipe', participant_id: 'p-1' })],
    })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('Felipe')
    expect(screen.queryByRole('button', { name: 'Identificar participante' })).not.toBeInTheDocument()
  })
})

describe('GroupsPage — Organização (navegar primeiro por Cliente do CRM)', () => {
  it('alternando para "Organização", lista os Clientes do CRM com a contagem de grupos e "Sem cliente vinculado"', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ,
      groups: [
        group({ id: 'g-1', name: 'Clientes InfoRR', account_id: 'acc-1', account_name: 'InfoRR Consultoria' }),
        group({ id: 'g-2', name: 'Suporte geral' }),
      ],
      accountsSummary: [{ account_id: 'acc-1', account_name: 'InfoRR Consultoria', group_count: 1 }],
    })
    renderAt(<GroupsPage />)
    await screen.findByRole('button', { name: /Clientes InfoRR/ })
    await user.click(screen.getByRole('radio', { name: 'Organização' }))
    const infoRR = await screen.findByRole('button', { name: /InfoRR Consultoria/ })
    expect(within(infoRR).getByText('1 grupo')).toBeInTheDocument()
    const unlinked = screen.getByRole('button', { name: /Sem cliente vinculado/ })
    expect(within(unlinked).getByText('1 grupo')).toBeInTheDocument()
    // a visão "Mensagens" some; os grupos não aparecem soltos nesse primeiro nível
    expect(screen.queryByRole('button', { name: /Clientes InfoRR/ })).not.toBeInTheDocument()
  })

  it('drill-down: clicar num cliente mostra só os grupos dele (primário + adicional, caso "swap"), e clicar num grupo abre a conversa de leitura de sempre', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ,
      groups: [
        group({ id: 'g-1', name: 'Clientes InfoRR', account_id: 'acc-1', account_name: 'InfoRR Consultoria' }),
        group({
          id: 'g-2',
          name: 'Swap - InfoRR x IP Fibra',
          account_id: 'acc-1',
          account_name: 'InfoRR Consultoria',
          additional_accounts: [{ id: 'acc-2', name: 'IP Fibra' }],
        }),
        group({ id: 'g-3', name: 'Suporte geral' }),
      ],
      accountsSummary: [
        { account_id: 'acc-1', account_name: 'InfoRR Consultoria', group_count: 2 },
        { account_id: 'acc-2', account_name: 'IP Fibra', group_count: 1 },
      ],
      messages: [],
    })
    renderAt(<GroupsPage />)
    await screen.findByRole('button', { name: /Clientes InfoRR/ })
    await user.click(screen.getByRole('radio', { name: 'Organização' }))
    await user.click(await screen.findByRole('button', { name: /IP Fibra/ }))
    // IP Fibra só concerne o grupo "swap" (ligado como adicional), nunca "Clientes InfoRR" nem "Suporte geral"
    expect(await screen.findByRole('button', { name: /Swap - InfoRR x IP Fibra/ })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Clientes InfoRR/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Suporte geral/ })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Swap - InfoRR x IP Fibra/ }))
    expect(await screen.findByText(/Somente leitura/)).toBeInTheDocument()
  })
})

describe('LinkGroupModal — "Vincular Grupo" (ativar + vincular cliente numa ação só)', () => {
  it('escolhe um grupo do WhatsApp e depois um Cliente do CRM, e chama ativar e vincular em sequência, nas rotas já existentes', async () => {
    const user = userEvent.setup()
    serve({
      permissions: MANAGE,
      groups: [],
      available: [{ provider_group_id: '120363000000000005@g.us', name: 'Nova Implantação', participant_count: 8, enabled: false, group_id: null }],
      crmCompanies: [{ id: 'co-1', name: 'InfoRR Consultoria', cnpj: '123' }],
    })
    vi.mocked(axios.post).mockResolvedValue({ data: group({ id: 'new-group', name: 'Nova Implantação' }) })
    vi.mocked(axios.put).mockResolvedValue({ data: {} })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Vincular Grupo' }))
    const dialog = await screen.findByRole('dialog', { name: 'Vincular Grupo' })
    await within(dialog).findByText('Nova Implantação')
    await user.click(within(dialog).getByRole('button', { name: 'Escolher' }))
    await within(dialog).findByText('InfoRR Consultoria')
    await user.click(within(dialog).getByRole('button', { name: /InfoRR Consultoria/ }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ provider_group_id: '120363000000000005@g.us' })
    await waitFor(() => expect(axios.put).toHaveBeenCalledTimes(1))
    expect(String(vi.mocked(axios.put).mock.calls[0][0])).toMatch(/\/groups\/new-group\/account$/)
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ directory_company_id: 'co-1' })
  })

  it('não oferece o botão sem group.manage', async () => {
    serve({ permissions: READ, groups: [] })
    renderAt(<GroupsPage />)
    await screen.findByText('Nenhum grupo habilitado')
    expect(screen.queryByRole('button', { name: 'Vincular Grupo' })).not.toBeInTheDocument()
  })
})

describe('ManageGroupsModal (via the page)', () => {
  const avail = (over: Partial<AvailableGroup> = {}): AvailableGroup => ({
    provider_group_id: '120363000000000001@g.us',
    name: 'Cobrança',
    participant_count: 5,
    enabled: false,
    group_id: null,
    ...over,
  })

  it('enables a group that OMNIRA does not know yet by its WhatsApp id', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [], available: [avail()] })
    vi.mocked(axios.post).mockResolvedValue({ data: group({ id: 'new', name: 'Cobrança' }) })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Gerenciar grupos' }))
    await user.click(await screen.findByRole('button', { name: 'Ativar Cobrança' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ provider_group_id: '120363000000000001@g.us' })
  })

  it('turns a known group off through its id, keeping the history', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [group()], available: [avail({ enabled: true, group_id: 'g-1', name: 'Oficial' })] })
    vi.mocked(axios.patch).mockResolvedValue({ data: group({ enabled: false }) })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Gerenciar grupos' }))
    expect(await screen.findByText('Lendo')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Desativar Oficial' }))
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.patch).mock.calls[0][0]).toMatch(/\/groups\/g-1$/)
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ enabled: false })
  })

  it('searches by name on the server and says when there are more than shown', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [], available: [avail(), avail({ provider_group_id: '120363000000000002@g.us', name: 'Amigos' })], availableTotal: 624 })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Gerenciar grupos' }))
    expect(await screen.findByText(/Mostrando 2 de 624/)).toBeInTheDocument()
    await user.type(screen.getByLabelText('Buscar grupo'), 'cobr')
    await waitFor(() => expect(screen.queryByText('Amigos')).not.toBeInTheDocument())
    expect(await screen.findByText('Cobrança')).toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some(([, c]) => (c as any)?.params?.q === 'cobr')).toBe(true)
  })

  it('shows the WhatsApp id so an operator can copy it (found live in production: nowhere else showed it)', async () => {
    const user = userEvent.setup()
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    serve({ permissions: MANAGE, groups: [], available: [avail()] })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Gerenciar grupos' }))
    expect(await screen.findByText('120363000000000001@g.us')).toBeInTheDocument()
    const copyButtons = screen.getAllByRole('button', { name: 'Copiar' })
    await user.click(copyButtons[copyButtons.length - 1]) // the row's own copy button (not the connection id one, when present)
    expect(writeText).toHaveBeenCalledWith('120363000000000001@g.us')
    expect(await screen.findByText('Copiado')).toBeInTheDocument()
  })

  it('explains a missing WhatsApp connection instead of failing silently', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [] })
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: MANAGE } }
      if (url.endsWith('/groups/available')) return Promise.reject({ response: { status: 409 } })
      return { data: { items: [] } }
    })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Gerenciar grupos' }))
    expect(await screen.findByText(/Não há uma conexão ativa do WhatsApp/)).toBeInTheDocument()
  })
})
