import { test, expect, Page, Route } from '@playwright/test';

// Organizador de grupos (ADR-0059, Gate 3) em um navegador real, com a API MOCKADA. O backend é provado contra
// PostgreSQL real por testes Go (grouporganizer_http_integration_test.go); aqui se confere a tela: pastas,
// Kanban SEM drag-and-drop (D-12 — mover é só pelo menu) e a confirmação explícita antes de qualquer escrita.

const HUB = '99999999-0000-0000-0000-000000000001';
const TENANT = '11111111-1111-1111-1111-111111111111';

type Writes = { method: string; path: string; body: any; idempotencyKey?: string }[];

function entry(id: string, name: string) {
  return { id, hub_id: HUB, source_tenant_id: 'A', source_channel_connection_id: 'conn-1', provider_group_id: '120300000000@g.us', display_name: name, source_state: 'available', version: 1, created_at: new Date().toISOString(), updated_at: new Date().toISOString(), last_seen_at: new Date().toISOString() };
}

async function install(page: Page) {
  const writes: Writes = [];
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  let links: Record<string, any[]> = {
    'g-unclassified': [],
    'g-alpha': [{ id: 'l1', hub_id: HUB, hub_group_id: 'g-alpha', tenant_id: 'A', role: 'consumer', status: 'active', assigned_at: new Date().toISOString(), version: 1, created_at: new Date().toISOString(), updated_at: new Date().toISOString() }],
  };
  await page.addInitScript(([t]) => {
    localStorage.setItem('token', 'mock-token');
    localStorage.setItem('tenantId', t);
    localStorage.setItem('sessionActive', 'true');
    localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'keslley@k3g.example', name: 'Keslley' }));
  }, [TENANT]);
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname.replace('/api/v1', '');
    const method = route.request().method();
    if (method !== 'GET') writes.push({ method, path: p, body: route.request().postData() ? route.request().postDataJSON() : null, idempotencyKey: route.request().headers()['idempotency-key'] });
    if (p === '/hubs') return json(route, { items: [{ id: HUB, name: 'K3G Solutions', role: 'hub_agent', can_manage_groups: true }] });
    if (p === `/hubs/${HUB}/managed`) return json(route, { items: [{ tenant_id: 'A', name: 'ISP Roraima', scopes: ['groups'] }, { tenant_id: 'B', name: 'NorteNet', scopes: ['groups'] }] });
    if (p === `/hubs/${HUB}/groups/catalog` && method === 'GET') return json(route, { items: [entry('g-unclassified', 'Suporte geral'), entry('g-alpha', 'Clientes Alpha')] });
    if (p === `/hubs/${HUB}/groups/relationships` && method === 'GET') return json(route, { items: [] });
    const linksMatch = /^\/hubs\/[^/]+\/groups\/catalog\/([^/]+)\/links$/.exec(p);
    if (linksMatch && method === 'GET') return json(route, { items: links[linksMatch[1]] ?? [] });
    if (linksMatch && method === 'POST') {
      const body = route.request().postDataJSON();
      const created = { id: 'l-new', hub_id: HUB, hub_group_id: linksMatch[1], tenant_id: body.tenant_id, role: body.role, status: 'active', assigned_at: new Date().toISOString(), version: 1, created_at: new Date().toISOString(), updated_at: new Date().toISOString() };
      links = { ...links, [linksMatch[1]]: [...(links[linksMatch[1]] ?? []), created] };
      return json(route, created, 201);
    }
    if (p.endsWith('/revoke') && method === 'POST') return route.fulfill({ status: 204 });
    return json(route, {}, 404);
  });
  return { writes };
}

test('a aba só aparece para quem tem o escopo "groups", e a lista mostra origem e "Sem classificação"', async ({ page }, info) => {
  await install(page);
  await page.goto('/admin/');
  await expect(page).toHaveURL(/\/admin\/grupos$/);
  await expect(page.getByRole('link', { name: 'Organizador de grupos' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Acessos' })).toHaveCount(0);
  const row = page.getByText('Suporte geral').locator('xpath=ancestor::tr');
  await expect(row.getByText('Sem classificação')).toBeVisible();
  await page.screenshot({ path: info.outputPath('group-organizer-list.png'), fullPage: true });
});

test('vincular: abre modal, pede confirmação explícita e manda Idempotency-Key — nada antes do clique final', async ({ page }) => {
  const { writes } = await install(page);
  await page.goto('/admin/grupos');
  const row = page.getByText('Suporte geral').locator('xpath=ancestor::tr');
  await row.getByRole('button', { name: 'Vincular a um Tenant' }).click();
  const dialog = page.getByRole('dialog', { name: 'Vincular a um Tenant' });
  await expect(dialog.getByRole('button', { name: 'Vincular' })).toBeDisabled();
  expect(writes.some((w) => w.method === 'POST')).toBe(false);
  await dialog.locator('#link-tenant').selectOption('B');
  await dialog.getByRole('button', { name: 'Vincular' }).click();
  await expect(dialog).toHaveCount(0);
  await expect.poll(() => writes.some((w) => w.path.endsWith('/links') && w.method === 'POST')).toBe(true);
  const write = writes.find((w) => w.path.endsWith('/links') && w.method === 'POST')!;
  expect(write.body).toEqual({ tenant_id: 'B', role: 'consumer' });
  expect(write.idempotencyKey).toBeTruthy();
});

test('revogar: o menu do vínculo abre confirmação antes de mandar a versão esperada', async ({ page }) => {
  const { writes } = await install(page);
  await page.goto('/admin/grupos');
  const row = page.getByText('Clientes Alpha').locator('xpath=ancestor::tr');
  await row.getByRole('button', { name: 'Ações do vínculo com ISP Roraima' }).click();
  await page.getByRole('menuitem', { name: 'Revogar este vínculo' }).click();
  const dialog = page.getByRole('dialog', { name: 'Revogar vínculo' });
  expect(writes.some((w) => w.method === 'POST')).toBe(false);
  await dialog.getByRole('button', { name: 'Revogar' }).click();
  await expect(dialog).toHaveCount(0);
  await expect.poll(() => writes.some((w) => w.path.endsWith('/links/A/revoke'))).toBe(true);
  expect(writes.find((w) => w.path.endsWith('/links/A/revoke'))!.body).toEqual({ expected_version: 1 });
});

test('kanban: nenhum elemento é arrastável — mover é só pelo menu (decisão D-12, sem DnD nesta fatia)', async ({ page }, info) => {
  await install(page);
  await page.goto('/admin/grupos');
  await page.getByRole('tab', { name: /^Kanban/ }).click();
  await expect(page.getByRole('list', { name: 'Quadro de grupos por pasta' })).toBeVisible();
  expect(await page.locator('[draggable="true"]').count()).toBe(0);
  await page.screenshot({ path: info.outputPath('group-organizer-kanban.png'), fullPage: true });
});
