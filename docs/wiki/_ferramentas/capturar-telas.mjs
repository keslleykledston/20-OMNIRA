// Tira as capturas de tela da documentação a partir do app construído (web/dist) servido por `vite preview`, com a API SIMULADA e dados fictícios
// (dados-ficticios.mjs). Nada aqui fala com o servidor real nem usa dados de cliente.
//
//   cd web && npx vite build && npx vite preview --host 127.0.0.1 --port 3190 --strictPort &
//   node docs/wiki/_ferramentas/capturar-telas.mjs [cena ...]        (sem argumento: todas)
import { createRequire } from 'node:module'
import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import * as D from './dados-ficticios.mjs'
import { QR_EXEMPLO } from './qr-exemplo.mjs'

const aqui = dirname(fileURLToPath(import.meta.url))
const require = createRequire(join(aqui, '../../../web/package.json'))
const { chromium } = require('@playwright/test')
const OUT = join(aqui, '..', 'imagens')
const BASE = process.env.BASE_URL || 'http://127.0.0.1:3190'
mkdirSync(OUT, { recursive: true })

const naoRespondido = new Set()
const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

import { ROLES } from './dados-ficticios.mjs'
const PERMISSOES = [...ROLES.find((r) => r.key === 'tenant_admin').permissions, 'flow.view', 'flow.create', 'flow_template.view', 'flow_template.install', 'flow_run.view'] || [
  'conversation.read', 'conversation.reply', 'conversation.claim', 'conversation.manage', 'conversation.transfer', 'conversation.finalize',
  'contact.read', 'contact.classify', 'contact.note', 'ticket.read', 'ticket.write', 'account.read', 'membership.read', 'integration.read', 'integration.manage',
  'tenant.read', 'dashboard.read', 'agent.read', 'agent.manage', 'channel.manage', 'channel.read', 'queue.manage', 'service.read', 'service.create', 'service.update', 'service.close', 'attendance.history',
]

async function instalar(page, extra = {}) {
  await page.addInitScript(([t, me]) => {
    localStorage.setItem('token', 'token-ficticio')
    localStorage.setItem('tenantId', t)
    localStorage.setItem('sessionActive', 'true')
    localStorage.setItem('user', JSON.stringify(me))
    localStorage.setItem('omnira.sidebar.collapsed', '0')
  }, [D.TENANT, D.ME])
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const p = url.pathname.replace('/api/v1', '')
    const m = route.request().method()
    if (/\/(events|presence)/.test(p) && !p.includes('/external')) return route.abort()
    const custom = extra.api?.(p, m, url, route)
    if (custom !== undefined) return custom === null ? route.fulfill({ status: 404, body: '' }) : json(route, custom)
    if (p.endsWith('/me/access')) return json(route, { role_key: 'tenant_admin', role_name: 'Administrador', permissions: PERMISSOES })
    if (p === '/tenants') return json(route, [{ id: D.TENANT, legal_name: 'Empresa Exemplo Ltda', trade_name: 'Empresa Exemplo' }])
    if (p === '/hubs' || p.endsWith('/hubs/mine')) return json(route, [])
    if (p.endsWith('/settings/inbox')) return json(route, { wait_warn_minutes: 30, wait_danger_minutes: 120 })
    if (/\/conversations\/v-ana\/service-session$/.test(p)) return json(route, D.SESSION_ANA)
    if (/\/inbox\/conversations\/[^/]+\/(topics|ambiguities)$/.test(p)) return json(route, { items: [] })
    if (/\/contacts\/[^/]+\/notes$/.test(p)) return json(route, { items: [] })
    if (/\/contacts\/c-[a-z]+$/.test(p)) return json(route, D.CONTACTS.find((c) => p.endsWith(c.id)) ?? null)
    if (p.endsWith('/inbox/settings') || p.includes('/inbox-settings')) return json(route, { wait_warn_minutes: 30, wait_danger_minutes: 120 })
    if (p.endsWith('/channels/lines')) return json(route, { items: [{ id: 'ch-1', provider: 'waha', status: 'active', label: 'WhatsApp principal', external_account_id: '5511900009999', can_send_text: true }] })
    if (/\/inbox\/conversations\/[^/]+\/channel$/.test(p)) return json(route, { can_send_text: true, window_required: false, window_open: true })
    if (/\/inbox\/conversations\/[^/]+\/messages$/.test(p) && m === 'GET') return json(route, { items: [...D.MESSAGES_ANA].reverse(), has_more: false })
    if (/\/inbox\/conversations\/[^/]+\/timeline$/.test(p)) return json(route, { items: D.TIMELINE_ANA })
    const conv = p.match(/\/inbox\/conversations\/([^/]+)$/)
    if (conv) { const c = D.CONVERSATIONS.find((x) => x.id === conv[1]); return c ? json(route, { ...c, message_count: 6 }) : route.fulfill({ status: 404, body: '' }) }
    if (p.endsWith('/inbox/conversations')) return json(route, { items: D.CONVERSATIONS, has_more: false })
    if (p.endsWith('/queues')) return json(route, { items: D.QUEUES })
    if (p.endsWith('/service-result-codes')) return json(route, { items: D.RESULT_CODES })
    if (/\/attendance-context$/.test(p)) return json(route, { contact_id: 'c-ana', attendances: [], open_follow_ups: [] })
    if (p.endsWith('/dashboard/snapshot')) return json(route, { open_conversations: 3, open_tickets: 4, total_contacts: 134, unclassified_contacts: 1 })
    if (p.endsWith('/integrations/apps') && m === 'POST') return json(route, { installation: { ...D.APPS[0], id: 'app-novo', name: 'CRM da Empresa Exemplo', credentials: [] }, credential: { id: 'cr-novo', key_id: 'exemplo123', label: 'Principal', created_at: D.T(0), expires_at: D.T(60 * 24 * 90), revoked_at: null, last_used_at: null, live: true, token: 'omn_ext_exemplo123.EXEMPLO-FICTICIO-NAO-E-UM-TOKEN-REAL' } }, 201)
    if (p.endsWith('/integrations/apps')) return json(route, { items: D.APPS, count: D.APPS.length })
    if (/\/integrations\/apps\/[^/]+\/webhooks$/.test(p)) return json(route, { items: [], count: 0 })
    if (/\/integrations\/apps\/[^/]+$/.test(p)) return json(route, D.APPS.find((a) => p.endsWith(a.id)) ?? null)
    if (p.endsWith('/channels/connections')) return json(route, { items: [{ id: 'ch-1', provider: 'waha', name: 'WhatsApp principal', status: 'active', external_account_id: '5511900009999', display_name: 'WhatsApp principal', created_at: D.T(-90000), updated_at: D.T(-90000) }] })
    if (/\/contacts\/[^/]+\/classification$/.test(p)) return json(route, { contact_id: 'c-ana', kind: 'customer', accounts: [] })
    if (p.endsWith('/tickets')) {
      const rank = { critical: 4, high: 3, medium: 2, low: 1 }
      const [campo, dir] = (url.searchParams.get('sort') || 'updated_at:desc').split(':')
      const chave = { priority: (t) => rank[t.priority], subject: (t) => t.subject.toLowerCase(), updated_at: (t) => t.updated_at }[campo] || ((t) => t.updated_at)
      const itens = [...D.TICKETS].sort((a, b) => (chave(a) < chave(b) ? -1 : chave(a) > chave(b) ? 1 : 0) * (dir === 'asc' ? 1 : -1))
      return json(route, { items: itens, has_more: false, count: itens.length, limit: 20, total: 37 })
    }
    if (p.endsWith('/people') || p.endsWith('/contacts')) {
      const q = (url.searchParams.get('q') || '').toLowerCase()
      let itens = D.CONTACTS.filter((c) => !q || c.display_name.toLowerCase().includes(q))
      if ((url.searchParams.get('sort') || '').startsWith('name')) itens = [...itens].sort((a, b) => a.display_name.localeCompare(b.display_name) * (url.searchParams.get('sort').endsWith('desc') ? -1 : 1))
      return json(route, { items: itens, has_more: !q && p.endsWith('/people'), next_cursor: 'x', count: itens.length, limit: 20, ...(p.endsWith('/people') ? { total: q ? itens.length : 134 } : {}) })
    }
    if (p.endsWith('/team')) return json(route, { items: D.TEAM })
    if (p.endsWith('/team/invitations')) return json(route, { items: D.INVITATIONS })
    if (p.endsWith('/roles')) return json(route, { items: D.ROLES })
    if (p.endsWith('/flows')) return json(route, { items: D.FLOWS, count: D.FLOWS.length })
    if (p.endsWith('/shifts')) return json(route, { items: [{ user_id: 'u-bruna', calendar_id: 'cal-1', queue_id: 'q-suporte' }, { user_id: 'u-caio', calendar_id: 'cal-2', queue_id: 'q-suporte' }] })
    if (p.endsWith('/on-call/rosters')) return json(route, { items: [{ id: 'ro-1', name: 'Plantão de fim de semana', kind: 'on_call', sector_id: 'sec-1', active: true, on_now: 0 }] })
    if (/\/queues\/[^/]+\/overflow-policy$/.test(p)) return json(route, { policy: { active: true, max_steps: 3, on_exhausted: 'keep_waiting', revision: 2, steps: [{ position: 1, name: 'Chamar o plantão', triggers: ['after_hours', 'sla_risk'], wait_seconds: 300, wait_business: true, destination_kind: 'queue', destination_id: 'q-suporte', allow_closed: false, allow_off_shift: false }] } })
    if (p.endsWith('/sla-policies')) return json(route, { items: [{ scope_kind: 'tenant', first_response_seconds: 600, queue_wait_seconds: 900, resolution_seconds: 28800, risk_percent: 80, counting: 'business', active: true, revision: 1 }] })
    if (/\/queues\/[^/]+\/eligibility$/.test(p)) return json(route, { queue_id: 'q-comercial', at: D.T(0), open: true, presence_required: false, eligible_count: 2, calendar_only: false, agents: [{ user_id: 'u-bruna', name: 'Bruna Alves', eligible: true, reasons: [], on_call: false, workload: 2, capacity: 5, shift_source: 'calendar' }, { user_id: 'u-caio', name: 'Caio Ferreira', eligible: false, reasons: ['capacity_full'], on_call: false, workload: 5, capacity: 5, shift_source: 'calendar' }] })
    if (p.endsWith('/hub-contracts')) return json(route, { items: [] })
    if (p.endsWith('/sectors')) return json(route, { items: D.SECTORS })
    if (p.endsWith('/calendars')) return json(route, { items: D.CALENDARS, default_timezone: 'America/Manaus', default_calendar_id: 'cal-1' })
    if (/\/queues\/[^/]+\/availability$/.test(p)) return json(route, { open: true, calendar_id: 'cal-1' })
    if (p.endsWith('/inbox/queue-options')) return json(route, { items: D.QUEUES.map(({ id, name, mode, is_default }) => ({ id, name, mode, is_default })) })
    if (/\/queues\/[^/]+\/sla$/.test(p)) return json(route, { queue_id: p.split('/')[p.split('/').length - 2], counts: p.includes('q-comercial') ? { ok: 0, at_risk: 1, breached: 0 } : { ok: 1, at_risk: 0, breached: 0 }, items: [] })
    if (p.endsWith('/agents')) return json(route, { items: [
      { id: 'ag-1', membership_id: 'mb-1', user_id: 'u-bruna', name: 'Bruna Alves', email: 'bruna@empresa-exemplo.test', role: 'tenant_agent', status: 'active', queues: [{ id: 'qa1', queue_id: 'q-suporte', queue_name: 'Suporte', available: true, capacity: 5 }] },
      { id: 'ag-2', membership_id: 'mb-2', user_id: 'u-caio', name: 'Caio Ferreira', email: 'caio@empresa-exemplo.test', role: 'tenant_agent', status: 'active', queues: [{ id: 'qa2', queue_id: 'q-suporte', queue_name: 'Suporte', available: true, capacity: 5 }] },
    ] })
    naoRespondido.add(`${m} ${p}`)
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{}' })
  })
}

async function nova(browser, { largura = 1440, altura = 900, extra } = {}) {
  const ctx = await browser.newContext({ viewport: { width: largura, height: altura }, locale: 'pt-BR', timezoneId: 'America/Manaus', deviceScaleFactor: 1 })
  const page = await ctx.newPage()
  await instalar(page, extra)
  return { ctx, page }
}
const esperar = (page, ms = 700) => page.waitForTimeout(ms)
const foto = (page, nome, opts = {}) => page.screenshot({ path: join(OUT, nome), ...opts })

async function entrar(page, caminho) {
  await page.goto(`${BASE}${caminho}`)
  await esperar(page, 1500)
}

const CENAS = {
  async conversas(b) {
    const { ctx, page } = await nova(b)
    await page.goto(`${BASE}/inbox`)
    await page.getByText('Perfeito, muito obrigada!').first().waitFor({ timeout: 15000 })
    await esperar(page, 1200)
    await foto(page, '01-conversas.png')
    await ctx.close()
  },
  async paleta(b) {
    const { ctx, page } = await nova(b, { extra: { api: (p, m, url) => (p.endsWith('/inbox/conversations') && url.searchParams.get('q') ? { items: D.CONVERSATIONS.filter((c) => c.contact_name.toLowerCase().includes(url.searchParams.get('q').toLowerCase())), has_more: false } : undefined) } })
    await entrar(page, '/tickets')
    await page.keyboard.press('Control+k')
    const caixa = page.getByRole('dialog', { name: 'Buscar' })
    await caixa.getByRole('combobox').fill('ana')
    await page.getByRole('option', { name: /Ana Souza/ }).first().waitFor({ timeout: 10000 })
    await esperar(page, 600)
    await foto(page, '02-paleta-ctrl-k.png')
    await ctx.close()
  },
  async finalizar(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/inbox')
    await page.getByRole('button', { name: 'Finalizar' }).first().click()
    await page.getByRole('dialog').first().waitFor({ timeout: 10000 })
    await esperar(page, 600)
    await foto(page, '03-finalizar.png')
    await ctx.close()
  },
  async transferir(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/inbox')
    await page.getByRole('button', { name: /Transferir/ }).first().click()
    await esperar(page, 1200)
    await foto(page, '04-transferir.png')
    await ctx.close()
  },
  async fila(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/inbox')
    await page.getByRole('button', { name: 'Mais ações do atendimento' }).first().click().catch(async () => { await page.locator('button[aria-label^="Mais ações"]').first().click() })
    await esperar(page, 500)
    await foto(page, '14-mais-acoes-atendimento.png', { clip: { x: 1120, y: 150, width: 320, height: 260 } })
    await page.getByRole('menuitem', { name: /fila/i }).first().click()
    await esperar(page, 1000)
    await foto(page, '15-enviar-para-fila.png')
    await ctx.close()
  },
  async linha_do_tempo(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/inbox')
    await foto(page, '16-linha-do-tempo.png', { clip: { x: 524, y: 330, width: 596, height: 480 } })
    await ctx.close()
  },
  async menu_contato(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/inbox')
    await page.getByRole('button', { name: 'Mais ações do contato' }).first().click()
    await page.getByRole('menuitem', { name: /spam/ }).waitFor({ timeout: 10000 })
    await foto(page, '05-contato-menu-spam.png', { clip: { x: 1120, y: 560, width: 320, height: 340 } })
    await ctx.close()
  },
  async tickets(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/tickets')
    await page.getByRole('columnheader', { name: /Prioridade/ }).getByRole('button').click()
    await esperar(page, 800)
    await foto(page, '06-tickets-ordenar.png')
    await ctx.close()
  },
  async tickets_celular(b) {
    const { ctx, page } = await nova(b, { largura: 390, altura: 844 })
    await entrar(page, '/tickets')
    await foto(page, '07-tickets-celular.png')
    await ctx.close()
  },
  async pessoas(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/contacts')
    await foto(page, '08-pessoas.png')
    await ctx.close()
  },
  async dashboard(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/')
    await foto(page, '09-dashboard.png')
    await ctx.close()
  },
  async integracoes(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/settings/integrations/apps')
    await foto(page, '10-integracoes.png')
    await page.getByRole('button', { name: /Nova aplicação/ }).first().click()
    await page.getByRole('dialog', { name: 'Nova aplicação externa' }).waitFor({ timeout: 10000 })
    await esperar(page, 600)
    await foto(page, '11-nova-aplicacao.png')
    await ctx.close()
  },
  async criar_aplicacao(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/settings/integrations/apps')
    await page.getByRole('button', { name: /Nova aplicação/ }).first().click()
    const painel = page.getByRole('dialog', { name: 'Nova aplicação externa' })
    await painel.getByLabel('Nome da aplicação').fill('CRM da Empresa Exemplo')
    for (const rotulo of ['Baixar arquivos das mensagens', 'Assumir e devolver conversas', 'Enviar mensagens ao cliente', 'Transferir conversas', 'Finalizar atendimentos']) await painel.getByLabel(rotulo).check()
    await esperar(page, 400)
    await foto(page, '17-aplicacao-escopos.png')
    await painel.getByRole('button', { name: 'Criar aplicação' }).click()
    await page.getByText('Novo token', { exact: false }).first().waitFor({ timeout: 10000 }).catch(() => {})
    await esperar(page, 800)
    await foto(page, '18-token-exibido-uma-vez.png')
    await ctx.close()
  },
  async gerenciar_aplicacao(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/settings/integrations/apps')
    await page.getByRole('button', { name: 'Gerenciar' }).first().click()
    await esperar(page, 1000)
    await foto(page, '19-gerenciar-aplicacao.png')
    await ctx.close()
  },
  async whatsapp(b) {
    let fase = 'novo'
    const conexao = () => ({ id: 'ch-novo', provider: 'waha', provider_kind: 'unofficial', status: fase === 'conectado' ? 'active' : 'pending', session_status: fase === 'conectado' ? 'working' : 'needs_qr', external_account_id: fase === 'conectado' ? '5511900008888' : undefined, capabilities: ['text'], created_at: D.T(0) })
    const { ctx, page } = await nova(b, { extra: { api: (p, m) => {
      if (p.endsWith('/channels/providers')) return { items: [{ id: 'waha', name: 'WhatsApp (não oficial)', channel: 'whatsapp', kind: 'unofficial', connect_method: 'qr_session', risk_notice: 'A automação não oficial pode causar o banimento do número. Use um número dedicado e aceite o risco antes de continuar.', capabilities: ['text'], enabled: true, inputs: [], displays: [] }] }
      if (/\/channels\/connections$/.test(p) && m === 'POST') return conexao()
      if (/\/session\/start$/.test(p)) return conexao()
      if (/\/connections\/ch-novo\/qr$/.test(p)) return { mimetype: 'image/png', data: QR_EXEMPLO }
      if (/\/channels\/connections\/ch-novo$/.test(p)) return conexao()
      return undefined
    } } })
    await entrar(page, '/channels/whatsapp/new')
    await page.getByRole('checkbox').check()
    await esperar(page, 400)
    await foto(page, '20-whatsapp-configuracao.png')
    await page.getByRole('button', { name: /Continuar/ }).click()
    await page.getByText('Escaneie o QR Code').waitFor({ timeout: 15000 })
    await esperar(page, 800)
    await foto(page, '21-whatsapp-qr.png')
    fase = 'conectado'
    await page.getByText(/Conectado|Concluído|conectado/).first().waitFor({ timeout: 20000 }).catch(() => {})
    await esperar(page, 1500)
    await foto(page, '22-whatsapp-conectado.png')
    await ctx.close()
  },
  async gestao_pessoas(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/settings/people')
    await foto(page, '23-pessoas-equipe.png')
    await ctx.close()
  },
  async papeis(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/settings/roles')
    await foto(page, '24-papeis.png')
    await ctx.close()
  },
  async automacao(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/flows')
    await foto(page, '25-automacao.png')
    await ctx.close()
  },
  async supervisor(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/supervisor')
    await foto(page, '26-supervisor.png')
    await ctx.close()
  },
  async horarios(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/settings/attendance/calendars')
    await foto(page, '27-horarios.png')
    await entrar(page, '/settings/attendance/shifts')
    await foto(page, '28-escalas.png')
    await entrar(page, '/settings/attendance/overflow')
    await foto(page, '29-transbordo-sla.png')
    await ctx.close()
  },
  async setores(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/settings/attendance/sectors')
    await foto(page, '12-setores.png')
    await ctx.close()
  },
  async canais(b) {
    const { ctx, page } = await nova(b)
    await entrar(page, '/channels')
    await foto(page, '13-canais.png')
    await ctx.close()
  },
}

const quais = process.argv.slice(2)
const browser = await chromium.launch({ headless: true })
try {
  for (const [nome, fn] of Object.entries(CENAS)) {
    if (quais.length && !quais.includes(nome)) continue
    try { await fn(browser); console.log('ok   ', nome) } catch (e) { console.log('FALHA', nome, String(e.message).split('\n')[0]) }
  }
} finally {
  await browser.close()
}
if (naoRespondido.size) console.log('\nChamadas sem resposta simulada (404):\n  ' + [...naoRespondido].join('\n  '))
