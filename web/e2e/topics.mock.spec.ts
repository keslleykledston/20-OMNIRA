import { test, expect, Page, Route } from '@playwright/test';

// The topic surface (ADR-0017) in a real browser against a mocked API. The scenario of the plan: open a conversation,
// see its topics, select one, read the logical timeline, resolve an ambiguous message, confirm the summary, ask the
// copilot for a draft (which must never send) and create a private-chat invite (shown once).

const TENANT = '11111111-1111-1111-1111-111111111111';
const CONV = 'd0d0d0d0-0000-0000-0000-000000000001';
const T1 = 'a1000000-0000-0000-0000-000000000001';
const T2 = 'a2000000-0000-0000-0000-000000000002';
const AMB = 'b1000000-0000-0000-0000-000000000001';
const SHOTS = 'test-results/topics-ui';

type Summary = { id: string; version: number; summary_text: string; status: string; authored_by: string; created_at: string };

async function openConversation(page: Page) {
  const panel = page.getByLabel('Assuntos da conversa');
  if (!(await panel.isVisible().catch(() => false))) {
    await page.getByText('Maria Souza').first().click();
  }
  await expect(panel).toBeVisible({ timeout: 15_000 });
}

function makeApi(page: Page) {
  const now = '2026-10-04T12:00:00Z';
  const state = {
    topics: [
      { id: T1, title: 'Pedido 837', status: 'open', privacy_policy: 'public', source: 'rule', last_activity_at: now, message_count: 3, ticket_count: 0 },
      { id: T2, title: 'Nota fiscal 992', status: 'open', privacy_policy: 'public', source: 'rule', last_activity_at: now, message_count: 1, ticket_count: 0 },
    ],
    ambiguities: [{ id: AMB, message_id: 'm-9', kind: 'conversation', created_at: now, candidates: [{ topic_id: T1, title: 'Pedido 837', score: 0.62 }, { topic_id: T2, title: 'Nota fiscal 992', score: 0.6 }] }],
    summaries: [{ id: 's1', version: 1, summary_text: 'Cliente relata que o pedido 837 não chegou há quatro dias.', status: 'ai_inferred', authored_by: 'machine', created_at: now }] as Summary[],
    posts: [] as string[],
    resolvedWith: '' as string,
  };
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const msgs = (id: string) =>
    id === T1
      ? [
          { id: 'm3', direction: 'outbound', message_type: 'text', body: 'Estamos verificando com a transportadora.', status: 'sent', created_at: '2026-10-04T12:10:00Z', relation: 'primary', decision_source: 'agent' },
          { id: 'm2', direction: 'inbound', message_type: 'text', body: 'já passaram quatro dias', status: 'received', created_at: '2026-10-04T12:05:00Z', relation: 'primary', decision_source: 'rule' },
          { id: 'm1', direction: 'inbound', message_type: 'text', body: 'meu pedido 837 não chegou', status: 'received', created_at: '2026-10-04T12:00:00Z', relation: 'primary', decision_source: 'entity' },
        ]
      : [{ id: 'm5', direction: 'inbound', message_type: 'text', body: 'a NF 992 está com valor errado', status: 'received', created_at: '2026-10-04T11:00:00Z', relation: 'primary', decision_source: 'entity' }];

  return {
    state,
    install: async () => {
      await page.addInitScript(([t]) => {
        localStorage.setItem('token', 'mock-token');
        localStorage.setItem('tenantId', t);
        localStorage.setItem('sessionActive', 'true');
        localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'agent@example.test', name: 'Atendente' }));
      }, [TENANT]);
      await page.route('**/api/v1/**', async (route) => {
        const req = route.request();
        const url = new URL(req.url());
        const p = url.pathname.replace('/api/v1/tenants/' + TENANT, '');
        const m = req.method();
        if (m !== 'GET') state.posts.push(`${m} ${p}`);
        if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
        if (p === '/me/access' || p.endsWith('/me/access')) return json(route, { permissions: ['topic.read', 'topic.manage', 'conversation.claim', 'ticket.read'], role_key: 'tenant_agent' });
        if (m === 'GET' && p === '/inbox/conversations') {
          return json(route, { items: [{ id: CONV, contact_name: 'Maria Souza', contact_phone: '+5511988887777', status: 'active', updated_at: now, last_message_at: now, last_message_direction: 'inbound', last_message_preview: 'já passaram quatro dias', assigned_to_user_id: 'u1' }], has_more: false });
        }
        if (m === 'GET' && p === `/inbox/conversations/${CONV}`) return json(route, { id: CONV, contact_name: 'Maria Souza', contact_phone: '+5511988887777', status: 'active', updated_at: now, message_count: 4, assigned_to_user_id: 'u1', contact_id: 'c1' });
        if (m === 'GET' && p === `/inbox/conversations/${CONV}/messages`) return json(route, { items: msgs(T1).reverse().map((x) => ({ ...x, conversation_id: CONV })), has_more: false });
        if (p.endsWith('/ticket')) return json(route, { local_ticket_id: 'lt-1', linked: false });
        if (p.endsWith('/crm/companies')) return json(route, { items: [] });
        // --- topic surface ---
        if (m === 'GET' && p === `/inbox/conversations/${CONV}/topics`) return json(route, { items: state.topics });
        if (m === 'GET' && p === `/inbox/conversations/${CONV}/ambiguities`) return json(route, { items: state.ambiguities });
        if (m === 'POST' && p === `/ambiguities/${AMB}/resolve`) {
          state.resolvedWith = req.postData() ?? '';
          state.ambiguities = [];
          state.topics[0].message_count = 4;
          return json(route, {});
        }
        let mt = p.match(/^\/topics\/([^/]+)\/(messages)$/);
        if (m === 'GET' && mt) return json(route, { items: msgs(mt[1]), has_more: false, count: 1, limit: 20 });
        mt = p.match(/^\/topics\/([^/]+)\/summaries$/);
        if (m === 'GET' && mt) return json(route, { items: mt[1] === T1 ? [...state.summaries].reverse() : [] });
        if (m === 'POST' && p === `/topics/${T1}/summary/confirm`) {
          state.summaries[state.summaries.length - 1].status = 'agent_confirmed';
          return json(route, state.summaries[state.summaries.length - 1]);
        }
        mt = p.match(/^\/topics\/([^/]+)\/tickets$/);
        if (m === 'GET' && mt) return json(route, { items: [] });
        mt = p.match(/^\/topics\/([^/]+)\/ticket-policy$/);
        if (m === 'GET' && mt) return json(route, { action: 'adopt_active', reason: 'O chamado ativo da conversa ainda não pertence a nenhum assunto.', allowed_actions: ['adopt_active', 'share_active'] });
        if (m === 'POST' && p === `/topics/${T1}/copilot/suggest-reply`) {
          return json(route, { reply: 'Olá, Maria! Estamos verificando o pedido 837 e já retornamos com a posição. Já cancelei a cobrança.', missing_info: ['confirmar o endereço de entrega'], needs_human: false, warnings: ['claims_action_done'], sent: false, model: 'mock', prompt_version: 'v1' });
        }
        if (m === 'POST' && p === `/topics/${T1}/handoffs`) {
          return json(route, { handoff: { id: 'h1', status: 'pending', expires_at: '2026-10-05T12:00:00Z' }, token: 'omn-' + 'A'.repeat(43), instructions: 'Peça ao cliente para enviar esta mensagem, exatamente como está, no chat privado com o número da empresa. O código vale uma única vez e expira.' }, 201);
        }
        return json(route, { error: 'not mocked' }, 404);
      });
    },
  };
}

test.describe('Painel de assuntos (ADR-0017)', () => {
  test('abrir conversa, ver assuntos, linha do tempo, resolver ambiguidade e confirmar resumo', async ({ page }) => {
    const api = makeApi(page);
    await api.install();
    await page.goto('/inbox');
    // the only conversation opens by itself; open it explicitly when the list does not
    await openConversation(page);

    // the topic panel sits beside the conversation, which stays visible
    const panel = page.getByLabel('Assuntos da conversa');
    await expect(panel).toBeVisible();
    await expect(page.getByText('já passaram quatro dias').first()).toBeVisible(); // the physical conversation is still there
    const list = panel.getByRole('list', { name: 'Lista de assuntos' });
    await expect(list.getByText('Pedido 837')).toBeVisible();
    await expect(list.getByText('Nota fiscal 992')).toBeVisible();

    // a message waits for a person to decide
    const waiting = panel.getByRole('region', { name: 'Aguardando decisão' });
    await expect(waiting).toBeVisible();
    await page.screenshot({ path: `${SHOTS}/01-assuntos-e-ambiguidade.png`, fullPage: false });
    await waiting.getByRole('button', { name: 'Pedido 837' }).click();
    await expect(waiting).toBeHidden();
    expect(api.state.resolvedWith).toContain(T1);

    // select a topic: logical timeline (oldest first) and the summary labelled as a machine draft
    await list.getByRole('button', { name: /Pedido 837/ }).click();
    const detail = panel.getByLabel('Detalhes do assunto Pedido 837');
    await expect(detail).toBeVisible();
    const timeline = detail.getByLabel('Linha do tempo do assunto');
    await expect(timeline.getByText('meu pedido 837 não chegou')).toBeVisible();
    const order = await timeline.locator('li p').allTextContents();
    expect(order[0]).toContain('meu pedido 837');
    expect(order[order.length - 1]).toContain('transportadora');
    await expect(detail.getByText('Rascunho da IA')).toBeVisible();
    await detail.getByRole('button', { name: 'Confirmar resumo' }).click();
    await expect(detail.getByText('Confirmado pelo atendente')).toBeVisible();
    await expect(detail.getByRole('button', { name: 'Confirmar resumo' })).toHaveCount(0);
    await page.screenshot({ path: `${SHOTS}/02-assunto-selecionado-resumo-confirmado.png`, fullPage: false });
  });

  test('copiloto só sugere (nunca envia) e o convite privado aparece uma única vez', async ({ page }) => {
    const api = makeApi(page);
    await api.install();
    await page.goto('/inbox');
    // the only conversation opens by itself; open it explicitly when the list does not
    await openConversation(page);
    const panel = page.getByLabel('Assuntos da conversa');
    await panel.getByRole('list', { name: 'Lista de assuntos' }).getByRole('button', { name: /Pedido 837/ }).click();
    const detail = panel.getByLabel('Detalhes do assunto Pedido 837');

    await detail.getByRole('button', { name: 'Sugerir resposta' }).click();
    await expect(detail.getByTestId('copilot-draft')).toContainText('Estamos verificando o pedido 837');
    await expect(detail.getByText(/não foi enviado/i)).toBeVisible();
    await expect(detail.getByText('Afirma que algo já foi feito')).toBeVisible();
    await expect(detail.getByText('confirmar o endereço de entrega')).toBeVisible();
    // suggesting never touches the message endpoints
    expect(api.state.posts.filter((x) => /\/messages|\/send/.test(x))).toEqual([]);
    expect(api.state.posts).toContain(`POST /topics/${T1}/copilot/suggest-reply`);
    await page.screenshot({ path: `${SHOTS}/03-copiloto-rascunho-com-avisos.png`, fullPage: false });

    await detail.getByRole('button', { name: 'Gerar convite' }).click();
    const token = detail.getByTestId('handoff-token');
    await expect(token).toHaveText(/^omn-A{43}$/);
    await expect(detail.getByText(/aparece só agora e vale uma única vez/)).toBeVisible();
    // nothing is kept in the browser
    const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
    expect(stored).not.toContain('omn-AAAA');
    // changing subject forgets the code
    await panel.getByRole('list', { name: 'Lista de assuntos' }).getByRole('button', { name: /Nota fiscal 992/ }).click();
    await expect(page.getByTestId('handoff-token')).toHaveCount(0);
    await page.screenshot({ path: `${SHOTS}/04-convite-privado.png`, fullPage: false });
  });

  test('em tela estreita a página não ganha rolagem horizontal', async ({ page }) => {
    const api = makeApi(page);
    await api.install();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/inbox');
    await expect(page.getByText('Maria Souza').first()).toBeAttached({ timeout: 15_000 });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(1);
  });
});
