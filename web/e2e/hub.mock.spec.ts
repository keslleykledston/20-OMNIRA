import { test, expect, Page, Route } from '@playwright/test';

// Hub (inbox agregada; leitura, e assumir/responder só com permissão) em um navegador real, com a API mockada. O backend do Hub é coberto por testes Go
// contra PostgreSQL real (RLS, HTTP, projetor); aqui se confere a tela: entrada no menu, lista com o nome da instância, abertura
// somente leitura, ausência de composer e de pedidos de mídia, e o comportamento no celular.

const TENANT = '11111111-1111-1111-1111-111111111111';
const HUB = '99999999-0000-0000-0000-000000000001';
const now = new Date().toISOString();
const item = (n: number, tenant: string, tenantName: string, over: Record<string, unknown> = {}) => ({
  id: `aaaaaaaa-0000-0000-0000-00000000000${n}`, tenant_id: tenant, tenant_name: tenantName, conversation_id: `cccccccc-0000-0000-0000-00000000000${n}`,
  customer_name: ['José Carlos', 'Maria Souza', 'Ana Lima'][n - 1], channel: 'whatsapp', status: 'open', priority: 'normal', unread_count: n === 1 ? 2 : 0, last_activity_at: now, ...over,
});
const ITEMS = [
  item(1, 'tenant-a', 'ISP Roraima'),
  item(2, 'tenant-b', 'NorteNet', { priority: 'high' }),
  item(3, 'tenant-a', 'ISP Roraima', { status: 'closed' }),
];

async function install(page: Page, opts: { hubs?: unknown; canReply?: boolean; writes?: { path: string; body: any; key?: string }[] } = {}) {
  const seen: string[] = [];
  let assignment: 'none' | 'me' = 'none';
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.addInitScript(([t]) => {
    localStorage.setItem('token', 'mock-token');
    localStorage.setItem('tenantId', t);
    localStorage.setItem('sessionActive', 'true');
    localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'maria@k3g.example', name: 'Maria' }));
  }, [TENANT]);
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname.replace('/api/v1', '');
    seen.push(p);
    if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
    if (p === '/hubs') return opts.hubs === 404 ? json(route, {}, 404) : json(route, { items: opts.hubs ?? [{ id: HUB, name: 'K3G Service Desk', role: 'hub_agent' }] });
    if (p === `/hubs/${HUB}/inbox`) {
      const companies = [...new Map(ITEMS.map((i) => [i.tenant_id, { id: i.tenant_id, name: i.tenant_name }])).values()];
      return json(route, { items: ITEMS, companies, has_more: false, count: ITEMS.length, limit: 30 });
    }
    const w = p.match(new RegExp(`^/hubs/${HUB}/inbox/([^/]+)/(claim|messages)$`));
    if (w && route.request().method() === 'POST') {
      const body = route.request().postDataJSON();
      opts.writes?.push({ path: p, body, key: route.request().headers()['idempotency-key'] });
      const it = ITEMS.find((i) => i.id === w[1]);
      if (!it) return json(route, {}, 404);
      if (w[2] === 'claim') {
        assignment = 'me';
        return json(route, { item_id: it.id, conversation_id: it.conversation_id, changed: true, tenant: { id: it.tenant_id, name: it.tenant_name } });
      }
      return route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify({ id: 'out-1', conversation_id: it.conversation_id, status: 'queued', tenant: { id: it.tenant_id, name: it.tenant_name } }) });
    }
    const m = p.match(new RegExp(`^/hubs/${HUB}/inbox/(.+)$`));
    if (m) {
      const it = ITEMS.find((i) => i.id === m[1]);
      if (!it) return json(route, {}, 404);
      return json(route, {
        item: it, tenant: { id: it.tenant_id, name: it.tenant_name }, access: { source: 'hub', can_reply: !!opts.canReply },
        conversation: { id: it.conversation_id, status: it.status, created_at: now, assignment },
        messages: [
          { id: 'm1', direction: 'inbound', message_type: 'text', body: 'Estou sem internet desde ontem à noite', status: 'received', created_at: now },
          { id: 'm2', direction: 'outbound', message_type: 'text', body: 'Já estamos verificando a sua conexão', status: 'sent', created_at: now },
          { id: 'm3', direction: 'inbound', message_type: 'image', body: '', status: 'received', created_at: now },
        ],
      });
    }
    if (p === '/tenants') return json(route, [{ id: TENANT, legal_name: 'K3G' }]);
    if (p.endsWith('/me/access')) return json(route, { permissions: [], role_key: 'tenant_agent' });
    return json(route, {}, 404);
  });
  return seen;
}

test('Conversas junta todas as instâncias liberadas (não há item "Hub" no menu), somente leitura', async ({ page }, info) => {
  const seen = await install(page);
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Hub', exact: true })).toHaveCount(0);
  await page.getByRole('link', { name: 'Conversas' }).first().click();
  await expect(page).toHaveURL(/\/inbox$/);
  await expect(page.getByRole('button', { name: 'Filtrar por instância' })).toContainText('Todas as instâncias');

  const list = page.getByRole('list', { name: 'Conversas do Hub' });
  await expect(list.getByText('José Carlos')).toBeVisible();
  await expect(list.getByText('NorteNet')).toBeVisible();
  await expect(list.getByText('ISP Roraima').first()).toBeVisible();
  await expect(list.getByText('Alta')).toBeVisible();
  await expect(list.getByText('Finalizado')).toBeVisible();
  await expect(list.getByRole('img', { name: 'WhatsApp' }).first()).toBeVisible();

  await list.getByText('Maria Souza').click();
  const header = page.getByRole('heading', { name: 'Maria Souza' }).locator('xpath=ancestor::header');
  await expect(header).toContainText('NorteNet');
  await expect(page.getByText('Estou sem internet desde ontem à noite')).toBeVisible();
  await expect(page.getByText('Imagem (não exibido no Hub)')).toBeVisible();
  await expect(page.getByRole('note')).toContainText('Somente leitura');
  await expect(page.getByRole('textbox')).toHaveCount(0);
  await page.screenshot({ path: info.outputPath('hub-desktop.png'), fullPage: false });

  expect(seen.filter((p) => p.includes('/media') || p.startsWith('/tenants/'))).toEqual(expect.not.arrayContaining(['/media']));
  expect(seen.some((p) => p.includes('/media'))).toBe(false);
});

test('sem Hub no servidor (404) o endereço antigo /hub leva a Conversas, que continua funcionando', async ({ page }) => {
  await install(page, { hubs: 404 });
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Conversas' }).first()).toBeVisible();
  await expect(page.getByRole('link', { name: 'Hub', exact: true })).toHaveCount(0);
  await page.goto('/inbox');
  await expect(page).toHaveURL(/\/inbox$/);
});

test('no celular: lista, depois só a conversa, com voltar', async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await install(page);
  await page.goto('/inbox');
  const list = page.getByRole('list', { name: 'Conversas do Hub' });
  await expect(list.getByText('Ana Lima')).toBeVisible();
  await page.screenshot({ path: info.outputPath('hub-mobile-list.png') });
  await expect(page.getByRole('navigation', { name: 'Navegação principal' }).getByRole('link', { name: 'Hub', exact: true })).toHaveCount(0);
  await list.getByText('José Carlos').click();
  await expect(page.getByText('Estou sem internet desde ontem à noite')).toBeVisible();
  await expect(list).toHaveCount(0);
  await page.screenshot({ path: info.outputPath('hub-mobile-detail.png') });
  await page.getByRole('button', { name: 'Voltar para a lista' }).click();
  await expect(page.getByRole('list', { name: 'Conversas do Hub' })).toBeVisible();
});

test('com permissão de resposta: assumir, ver "Respondendo como" e enviar com a instância da conversa', async ({ page }, info) => {
  const writes: { path: string; body: any; key?: string }[] = [];
  await install(page, { canReply: true, writes });
  await page.goto('/inbox');
  const list = page.getByRole('list', { name: 'Conversas do Hub' });
  await list.getByText('Maria Souza').click();
  await expect(page.getByText('Assuma a conversa para responder como NorteNet.')).toBeVisible();
  await expect(page.getByRole('textbox')).toHaveCount(0);

  await page.getByRole('button', { name: 'Assumir' }).click();
  await expect(page.getByText('Respondendo como NorteNet')).toBeVisible();
  await page.getByRole('textbox').fill('Olá Maria, já estamos verificando');
  await page.getByRole('button', { name: 'Enviar mensagem' }).click();
  await expect.poll(() => writes.length).toBe(2);
  await page.screenshot({ path: info.outputPath('hub-reply.png') });

  expect(writes[0].path).toMatch(/\/claim$/);
  expect(writes[0].body).toEqual({ expected_tenant_id: 'tenant-b' });
  expect(writes[1].path).toMatch(/\/messages$/);
  expect(writes[1].body).toEqual({ expected_tenant_id: 'tenant-b', text: 'Olá Maria, já estamos verificando' });
  expect(writes[1].key).toBeTruthy();
});

test('somente leitura: nenhum botão de assumir nem caixa de texto', async ({ page }) => {
  const writes: { path: string; body: any }[] = [];
  await install(page, { canReply: false, writes });
  await page.goto('/inbox');
  await page.getByRole('list', { name: 'Conversas do Hub' }).getByText('Maria Souza').click();
  await expect(page.getByRole('note')).toContainText('Somente leitura');
  await expect(page.getByRole('button', { name: 'Assumir' })).toHaveCount(0);
  await expect(page.getByRole('textbox')).toHaveCount(0);
  expect(writes).toEqual([]);
});
