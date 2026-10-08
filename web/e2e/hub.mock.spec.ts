import { test, expect, Page, Route } from '@playwright/test';

// Hub (inbox agregada, somente leitura) em um navegador real, com a API mockada. O backend do Hub é coberto por testes Go
// contra PostgreSQL real (RLS, HTTP, projetor); aqui se confere a tela: entrada no menu, lista com o nome da empresa, abertura
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

async function install(page: Page, opts: { hubs?: unknown } = {}) {
  const seen: string[] = [];
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
    if (p === `/hubs/${HUB}/inbox`) return json(route, { items: ITEMS, has_more: false, count: ITEMS.length, limit: 30 });
    const m = p.match(new RegExp(`^/hubs/${HUB}/inbox/(.+)$`));
    if (m) {
      const it = ITEMS.find((i) => i.id === m[1]);
      if (!it) return json(route, {}, 404);
      return json(route, {
        item: it, tenant: { id: it.tenant_id, name: it.tenant_name }, access: { source: 'hub' },
        conversation: { id: it.conversation_id, status: it.status, created_at: now },
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

test('o Hub aparece no menu e abre a inbox agregada, somente leitura', async ({ page }, info) => {
  const seen = await install(page);
  await page.goto('/');
  const link = page.getByRole('link', { name: 'Hub' });
  await expect(link).toBeVisible();
  await link.click();
  await expect(page).toHaveURL(/\/hub$/);

  const list = page.getByRole('list', { name: 'Conversas do Hub' });
  await expect(list.getByText('José Carlos')).toBeVisible();
  await expect(list.getByText('NorteNet')).toBeVisible();
  await expect(list.getByText('ISP Roraima').first()).toBeVisible();
  await expect(list.getByText('Alta')).toBeVisible();
  await expect(list.getByText('Finalizado')).toBeVisible();

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

test('sem Hub no servidor (404) o menu não mostra o Hub e a página explica', async ({ page }) => {
  await install(page, { hubs: 404 });
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Conversas' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Hub' })).toHaveCount(0);
  await page.goto('/hub');
  await expect(page.getByText('Hub indisponível')).toBeVisible();
});

test('no celular: lista, depois só a conversa, com voltar', async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await install(page);
  await page.goto('/hub');
  const list = page.getByRole('list', { name: 'Conversas do Hub' });
  await expect(list.getByText('Ana Lima')).toBeVisible();
  await page.screenshot({ path: info.outputPath('hub-mobile-list.png') });
  await expect(page.getByRole('navigation', { name: 'Navegação principal' }).getByRole('link', { name: 'Hub' })).toBeVisible();
  await list.getByText('José Carlos').click();
  await expect(page.getByText('Estou sem internet desde ontem à noite')).toBeVisible();
  await expect(list).toHaveCount(0);
  await page.screenshot({ path: info.outputPath('hub-mobile-detail.png') });
  await page.getByRole('button', { name: 'Voltar para a lista' }).click();
  await expect(page.getByRole('list', { name: 'Conversas do Hub' })).toBeVisible();
});
