import { test, expect, Page, Route } from '@playwright/test';

// Painel de Acessos (ADR-0039) e "Conversas" unificadas em um navegador real, com a API MOCKADA. O backend é provado por testes Go contra
// PostgreSQL real; aqui se confere a tela: quem vê o quê, a matriz, e o filtro de empresas no lugar do menu de canais.

const TENANT = '11111111-1111-1111-1111-111111111111';
const HUB = '99999999-0000-0000-0000-000000000001';

const person = (id: string, email: string, name = '') => ({ user_id: id, email, name });
const overview = () => ({
  hub_id: HUB, hub_name: 'K3G Solutions',
  instances: [
    { tenant_id: 'A', name: 'ISP Roraima', tenant_status: 'active', contract_status: 'active', admins: [person('adm-a', 'adm@roraima.com', 'Ana')], direct_agents: 2, hub_agents: 1 },
    { tenant_id: 'B', name: 'NorteNet', tenant_status: 'active', contract_status: 'active', admins: [person('adm-b1', 'b1@nortenet.com'), person('adm-b2', 'b2@nortenet.com')], direct_agents: 1, hub_agents: 1 },
  ],
  agents: [
    { ...person('chefe', 'chefe@k3g.com', 'Chefe'), hub_role: 'hub_admin', grants: [], direct_instances: [], instances: 0 },
    { ...person('x', 'x@k3g.com', 'Xavier'), hub_role: 'hub_agent', grants: [{ tenant_id: 'A', mode: 'read' }], direct_instances: [], instances: 1 },
  ],
});

async function install(page: Page, opts: { admin: boolean; companies: { id: string; name: string }[] }) {
  const ov = { ...overview(), invitations: [] as { id: string; email: string; access: { tenant_id: string; mode: string }[]; created_at: string; expires_at: string }[] };
  const writes: { method: string; path: string; body: any }[] = [];
  const inboxQueries: string[] = [];
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
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
    if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
    if (p === '/hubs') return json(route, { items: [{ id: HUB, name: 'K3G Solutions', role: opts.admin ? 'hub_admin' : 'hub_agent', can_manage_companies: false, can_manage_access: opts.admin }] });
    if (p === `/hubs/${HUB}/inbox`) {
      inboxQueries.push(url.searchParams.get('companies') ?? '');
      const only = (url.searchParams.get('companies') ?? '').split(',').filter(Boolean);
      const all = [
        { id: 'i1', tenant_id: 'A', tenant_name: 'ISP Roraima', conversation_id: 'c1', customer_name: 'Cliente Alfa', channel: 'whatsapp', status: 'open', priority: 'normal', unread_count: 1 },
        { id: 'i2', tenant_id: 'B', tenant_name: 'NorteNet', conversation_id: 'c2', customer_name: 'Cliente Beta', channel: 'whatsapp', status: 'open', priority: 'normal', unread_count: 0 },
      ];
      const items = only.length ? all.filter((i) => only.includes(i.tenant_id)) : all;
      return json(route, { items, companies: opts.companies, has_more: false, count: items.length, limit: 30 });
    }
    if (p === `/hubs/${HUB}/access` && method === 'GET') return json(route, ov);
    if (p === `/hubs/${HUB}/access/invitations` && method === 'POST') {
      const body = route.request().postDataJSON();
      writes.push({ method, path: p, body });
      if (String(body.email).startsWith('conta@')) return json(route, { status: 'applied' });
      ov.invitations.push({ id: 'inv-1', email: body.email, access: body.access, created_at: '2026-10-09T00:00:00Z', expires_at: '2026-10-23T00:00:00Z' });
      return json(route, { status: 'pending', expires_at: '2026-10-23T00:00:00Z' });
    }
    const cancel = p.match(new RegExp(`^/hubs/${HUB}/access/invitations/([^/]+)$`));
    if (cancel && method === 'DELETE') {
      writes.push({ method, path: p, body: null });
      ov.invitations = ov.invitations.filter((i) => i.id !== cancel[1]);
      return route.fulfill({ status: 204 });
    }
    if (p === `/hubs/${HUB}/access/agents` && method === 'POST') {
      const body = route.request().postDataJSON();
      writes.push({ method, path: p, body });
      return json(route, person('new', body.email));
    }
    const cell = p.match(new RegExp(`^/hubs/${HUB}/access/agents/([^/]+)/instances/([^/]+)$`));
    if (cell && method === 'PUT') {
      const body = route.request().postDataJSON();
      writes.push({ method, path: p, body });
      const agent = ov.agents.find((a) => a.user_id === cell[1]);
      if (!agent) return json(route, {}, 404);
      agent.grants = agent.grants.filter((g) => g.tenant_id !== cell[2]);
      if (body.mode !== 'none') agent.grants.push({ tenant_id: cell[2], mode: body.mode } as never);
      agent.instances = agent.grants.length;
      return route.fulfill({ status: 204 });
    }
    if (p === '/tenants') return json(route, [{ id: TENANT, legal_name: 'K3G' }]);
    if (p.endsWith('/me/access')) return json(route, { permissions: [], role_key: 'tenant_admin' });
    return json(route, {}, 404);
  });
  return { writes, inboxQueries };
}

test('o administrador do Hub vê a matriz, libera um agente em uma segunda instância e o painel conta isso', async ({ page }) => {
  const { writes } = await install(page, { admin: true, companies: [] });
  await page.goto('/hub');
  await page.getByRole('link', { name: 'Acessos' }).first().click();
  await expect(page).toHaveURL(/\/acessos$/);

  const sel = page.getByLabel('Acesso de x@k3g.com em NorteNet');
  await expect(sel).toHaveValue('none');
  const rowX = page.getByRole('row', { name: /Xavier/ });
  await expect(rowX.getByText('Uma instância')).toBeVisible();

  await sel.selectOption('reply');
  await expect(sel).toHaveValue('reply');
  await expect(rowX.getByText('2 instâncias')).toBeVisible();
  expect(writes.some((w) => w.method === 'PUT' && w.path.endsWith('/agents/x/instances/B') && w.body.mode === 'reply')).toBe(true);

  // administradores por instância
  await page.getByRole('tab', { name: 'Instâncias e administradores' }).click();
  await expect(page.getByRole('article', { name: 'Instância ISP Roraima' }).getByText('Ana')).toBeVisible();
  await expect(page.getByRole('article', { name: 'Instância NorteNet' }).getByText('b2@nortenet.com')).toBeVisible();
});

test('o administrador autoriza por e-mail quem ainda não tem conta, vê a autorização aguardando e a cancela', async ({ page }) => {
  const { writes } = await install(page, { admin: true, companies: [] });
  await page.goto('/acessos');
  await page.getByLabel('Adicionar pessoa ao Hub').fill('Futuro@Nova.com');
  await page.getByLabel('Acesso inicial em NorteNet').selectOption('reply');
  await page.getByRole('button', { name: 'Adicionar' }).click();
  await expect(page.getByRole('alert')).toContainText('ainda não tem conta');
  const waiting = page.getByRole('region', { name: 'Autorizações aguardando o primeiro acesso' });
  await expect(waiting.getByText('Futuro@Nova.com')).toBeVisible();
  await expect(waiting.getByText(/NorteNet: Ler e responder/)).toBeVisible();
  const sent = writes.find((w) => w.path.endsWith('/access/invitations'))!;
  expect(sent.body).toEqual({ email: 'Futuro@Nova.com', access: [{ tenant_id: 'B', mode: 'reply' }] });

  await waiting.getByRole('button', { name: 'Cancelar a autorização de Futuro@Nova.com' }).click();
  await expect(waiting).toHaveCount(0);
  expect(writes.some((w) => w.method === 'DELETE' && w.path.endsWith('/access/invitations/inv-1'))).toBe(true);

  await page.getByLabel('Adicionar pessoa ao Hub').fill('conta@k3g.com');
  await page.getByRole('button', { name: 'Adicionar' }).click();
  await expect(page.getByRole('alert')).toContainText('já tinha conta');
});

test('quem não administra o Hub não vê o painel nem o link para ele', async ({ page }) => {
  await install(page, { admin: false, companies: [] });
  await page.goto('/hub');
  await expect(page.getByRole('link', { name: 'Acessos' })).toHaveCount(0);
  await page.goto('/acessos');
  await expect(page.getByText('Painel de acessos indisponível')).toBeVisible();
});

test('Conversas unificadas: quem atende duas empresas vê tudo e filtra por empresa no lugar do menu de canais', async ({ page }) => {
  const { inboxQueries } = await install(page, { admin: false, companies: [{ id: 'A', name: 'ISP Roraima' }, { id: 'B', name: 'NorteNet' }] });
  await page.goto('/inbox');
  await expect(page.getByRole('heading', { name: 'Conversas' })).toBeVisible();
  await expect(page.getByText('Cliente Alfa')).toBeVisible();
  await expect(page.getByText('Cliente Beta')).toBeVisible();
  await expect(page.getByRole('combobox', { name: /canal/i })).toHaveCount(0);

  const filter = page.getByRole('button', { name: 'Filtrar por empresa' });
  await expect(filter).toContainText('Todas as empresas');
  await filter.click();
  await page.getByLabel('NorteNet').check();
  await expect(page.getByText('Cliente Alfa')).toHaveCount(0);
  await expect(page.getByText('Cliente Beta')).toBeVisible();
  await expect(filter).toContainText('NorteNet');
  expect(inboxQueries).toContain('B');

  await page.getByLabel('Todas as empresas').check();
  await expect(page.getByText('Cliente Alfa')).toBeVisible();
});
