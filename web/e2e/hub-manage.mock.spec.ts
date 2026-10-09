import { test, expect, Page, Route } from '@playwright/test';

// Gestão delegada de canais, equipes e transferência (ADR-0038 fases 3 e 4) em um navegador real, com a API MOCKADA. O backend é provado
// por testes Go contra PostgreSQL real; aqui se confere a tela: o que cada pessoa vê, a mesma tela de Canais apontada para o Hub,
// as equipes e o diálogo de transferência.

const TENANT = '11111111-1111-1111-1111-111111111111';
const HUB = '99999999-0000-0000-0000-000000000001';
const person = (id: string, email: string, name = '') => ({ user_id: id, email, name });

type Writes = { method: string; path: string; body: any }[];

async function install(page: Page, opts: { role: 'hub_admin' | 'hub_agent'; operator?: boolean; manages?: boolean }) {
  const writes: Writes = [];
  const gets: string[] = [];
  let scopes: string[] = [];
  let pools: any[] = [{ id: 'p1', name: 'Suporte', description: '', distribution: 'manual', members: [], instances: [] }];
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.addInitScript(([t]) => {
    localStorage.setItem('token', 'mock-token');
    localStorage.setItem('tenantId', t);
    localStorage.setItem('sessionActive', 'true');
    localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'keslley@k3g.example', name: 'Keslley' }));
  }, [TENANT]);
  const company = () => ({
    id: 'A', legal_name: 'ISP Roraima Ltda', trade_name: 'ISP Roraima', display_name: 'ISP Roraima', status: 'active', contract_status: 'active',
    capabilities: {}, channels: 1, integrations: 0, open_conversations: 2, agents: 1, management_scopes: scopes, created_at: new Date().toISOString(),
  });
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname.replace('/api/v1', '');
    const method = route.request().method();
    if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
    if (method === 'GET') gets.push(p);
    if (method !== 'GET') writes.push({ method, path: p, body: route.request().postData() ? route.request().postDataJSON() : null });
    if (p === '/hubs') {
      return json(route, { items: [{ id: HUB, name: 'K3G Solutions', role: opts.role, can_manage_access: opts.role === 'hub_admin', can_manage_companies: !!opts.operator, can_manage_instances: opts.role === 'hub_admin' || !!opts.manages }] });
    }
    if (p === `/hubs/${HUB}/inbox`) {
      return json(route, { items: [{ id: 'i1', tenant_id: 'A', tenant_name: 'ISP Roraima', conversation_id: 'c1', customer_name: 'Cliente Alfa', channel: 'whatsapp', status: 'open', priority: 'normal', unread_count: 0 }], companies: [{ id: 'A', name: 'ISP Roraima' }, { id: 'B', name: 'NorteNet' }], has_more: false, count: 1, limit: 30 });
    }
    if (p === `/hubs/${HUB}/inbox/i1`) {
      return json(route, {
        item: { id: 'i1', tenant_id: 'A', tenant_name: 'ISP Roraima', conversation_id: 'c1', customer_name: 'Cliente Alfa', channel: 'whatsapp', status: 'open', priority: 'normal', unread_count: 0 },
        tenant: { id: 'A', name: 'ISP Roraima' }, access: { source: 'hub', can_reply: true },
        conversation: { id: 'c1', status: 'open', created_at: new Date().toISOString(), assignment: 'me' },
        messages: [{ id: 'm1', direction: 'inbound', message_type: 'text', body: 'Estou sem internet', status: 'received', created_at: new Date().toISOString() }],
      });
    }
    if (p === `/hubs/${HUB}/inbox/i1/transfer-candidates`) return json(route, { items: [{ user_id: 'u-beto', name: 'Beto', email: 'beto@k3g.com', load: 2 }, { user_id: 'u-cris', name: '', email: 'cris@k3g.com', load: 0 }] });
    if (p === `/hubs/${HUB}/inbox/i1/transfer`) return json(route, { conversation_id: 'c1', assigned_to: route.request().postDataJSON().to_user_id, tenant: { id: 'A', name: 'ISP Roraima' } });
    if (p === `/hubs/${HUB}/companies` && method === 'GET') return json(route, { items: [company()], capabilities: [] });
    if (p === `/hubs/${HUB}/companies/A` && method === 'PATCH') {
      const b = route.request().postDataJSON();
      if (b.management_scopes) scopes = b.management_scopes;
      return json(route, company());
    }
    if (p === `/hubs/${HUB}/access`) {
      return json(route, {
        hub_id: HUB, hub_name: 'K3G Solutions', invitations: [],
        instances: [{ tenant_id: 'A', name: 'ISP Roraima', tenant_status: 'active', contract_status: 'active', admins: [person('adm-a', 'adm@roraima.com', 'Ana')], direct_agents: 1, hub_agents: 1, management_scopes: scopes }],
        agents: [{ ...person('u-beto', 'beto@k3g.com', 'Beto'), hub_role: 'hub_agent', grants: [{ tenant_id: 'A', mode: 'reply', can_manage: false }], direct_instances: [], instances: 1 }],
      });
    }
    if (p.endsWith('/management') && method === 'PUT') return route.fulfill({ status: 204 });
    if (p === `/hubs/${HUB}/pools` && method === 'GET') return json(route, { items: pools });
    if (p === `/hubs/${HUB}/pools` && method === 'POST') {
      const b = route.request().postDataJSON();
      const created = { id: 'p2', name: b.name, description: '', distribution: b.distribution, members: [], instances: [] };
      pools = [...pools, created];
      return json(route, created, 201);
    }
    if (p.startsWith(`/hubs/${HUB}/pools/`) && (method === 'PUT' || method === 'PATCH' || method === 'DELETE')) return route.fulfill({ status: 204 });
    if (p === `/hubs/${HUB}/managed`) return json(route, { items: [{ tenant_id: 'A', name: 'ISP Roraima', scopes: ['channels', 'integrations'] }] });
    if (p === `/hubs/${HUB}/instances/A/channels/providers`) {
      return json(route, { items: [{ id: 'waha', name: 'WhatsApp (não oficial)', channel: 'whatsapp', kind: 'unofficial', connect_method: 'qr_session', capabilities: ['text'], enabled: true, inputs: [], displays: [] }] });
    }
    if (p === `/hubs/${HUB}/instances/A/channels/connections` && method === 'GET') {
      return json(route, { items: [{ id: 'conn-1', provider: 'waha', provider_kind: 'unofficial', status: 'active', session_status: 'working', external_account_id: '5595999990000', capabilities: ['text'], created_at: new Date().toISOString() }] });
    }
    if (p === `/hubs/${HUB}/instances/A/channels/connections/conn-1`) {
      return json(route, { id: 'conn-1', provider: 'waha', provider_kind: 'unofficial', status: 'active', session_status: 'working', external_account_id: '5595999990000', capabilities: ['text'], created_at: new Date().toISOString() });
    }
    if (p === '/tenants') return json(route, [{ id: TENANT, legal_name: 'K3G' }]);
    if (p.endsWith('/me/access')) return json(route, { permissions: [], role_key: 'tenant_agent' });
    return json(route, {}, 404);
  });
  return { writes, gets };
}

test('o operador delega canais e integrações no cartão da instância, e o link para as telas aparece', async ({ page }) => {
  const { writes } = await install(page, { role: 'hub_admin', operator: true });
  await page.goto('/acessos?aba=instancias');
  const card = page.getByRole('region', { name: 'Instância ISP Roraima' });
  await expect(card.getByLabel(/Canais de atendimento/)).not.toBeChecked();
  await expect(card.getByRole('link', { name: 'Abrir canais e integrações' })).toHaveCount(0); // nada delegado ainda
  await card.getByLabel(/Canais de atendimento/).click(); // controlled by the server's answer, so no .check()
  await expect(card.getByLabel(/Canais de atendimento/)).toBeChecked();
  await expect(card.getByRole('link', { name: 'Abrir canais e integrações' })).toHaveAttribute('href', `/instancias/${HUB}/A/canais`);
  expect(writes.find((w) => w.method === 'PATCH')!.body).toEqual({ management_scopes: ['channels'] });
});

test('quem gerencia vê "Canais das instâncias" e usa a MESMA tela de Canais, falando com as rotas do Hub', async ({ page }) => {
  const { gets } = await install(page, { role: 'hub_agent', manages: true });
  await page.goto('/inbox');
  await page.getByRole('link', { name: 'Canais das instâncias' }).first().click();
  await expect(page).toHaveURL(/\/instancias$/);
  await page.getByRole('link', { name: 'Abrir canais e integrações' }).click();
  await expect(page).toHaveURL(new RegExp(`/instancias/${HUB}/A/canais$`));
  await expect(page.getByRole('heading', { name: /Canais e integrações — ISP Roraima/ })).toBeVisible();
  await expect(page.getByText('WhatsApp (não oficial)').first()).toBeVisible();
  expect(gets.some((p) => p === `/hubs/${HUB}/instances/A/channels/connections`)).toBe(true);
  expect(gets.some((p) => p.startsWith('/tenants/') && p.includes('/channels'))).toBe(false); // nunca as rotas da empresa
});

test('a chave "Gerenciar" na matriz só aparece onde o contrato delega e envia exatamente aquele grant', async ({ page }) => {
  const { writes } = await install(page, { role: 'hub_admin', operator: true });
  await page.goto('/acessos?aba=instancias');
  await page.getByRole('region', { name: 'Instância ISP Roraima' }).getByLabel(/Canais de atendimento/).click();
  await expect(page.getByRole('region', { name: 'Instância ISP Roraima' }).getByLabel(/Canais de atendimento/)).toBeChecked();
  await page.getByRole('tab', { name: 'Agentes e permissões' }).click();
  const box = page.getByLabel('Gerenciar ISP Roraima — beto@k3g.com');
  await expect(box).toBeVisible();
  await box.click();
  await expect.poll(() => writes.some((w) => w.method === 'PUT' && w.path.endsWith('/access/agents/u-beto/instances/A/management') && w.body.can_manage === true)).toBe(true);
});

test('equipes: criar, escolher integrantes com capacidade e instâncias, e ligar a distribuição automática', async ({ page }) => {
  const { writes } = await install(page, { role: 'hub_admin' });
  await page.goto('/acessos?aba=equipes');
  await page.getByRole('textbox', { name: 'Nova equipe' }).fill('Vendas');
  await page.getByRole('button', { name: 'Criar equipe' }).click();
  await expect(page.getByRole('alert')).toContainText('Equipe criada');
  expect(writes.find((w) => w.method === 'POST')!.body).toEqual({ name: 'Vendas', distribution: 'manual' });

  const team = page.getByRole('article', { name: 'Equipe Suporte' });
  await team.getByLabel('beto@k3g.com na equipe Suporte').check();
  await team.getByLabel('Capacidade de beto@k3g.com na equipe Suporte').fill('5');
  await team.getByRole('button', { name: 'Salvar integrantes' }).click();
  await expect.poll(() => writes.some((w) => w.method === 'PUT' && w.path.endsWith('/pools/p1/members'))).toBe(true);
  expect(writes.find((w) => w.path.endsWith('/pools/p1/members'))!.body).toEqual({ members: [{ user_id: 'u-beto', max_open: 5 }] });

  await team.getByLabel('ISP Roraima atendida pela equipe Suporte').check();
  await team.getByRole('button', { name: 'Salvar instâncias' }).click();
  await expect.poll(() => writes.some((w) => w.path.endsWith('/pools/p1/instances'))).toBe(true);
  expect(writes.find((w) => w.path.endsWith('/pools/p1/instances'))!.body).toEqual({ instances: [{ tenant_id: 'A' }] });

  await team.getByLabel('Distribuição da equipe Suporte').selectOption('round_robin');
  await expect.poll(() => writes.some((w) => w.method === 'PATCH' && w.body?.distribution === 'round_robin')).toBe(true);
});

test('transferir: o titular escolhe quem recebe e a tela envia a pessoa com a instância exibida', async ({ page }, info) => {
  const { writes } = await install(page, { role: 'hub_agent' });
  await page.goto('/inbox');
  await page.getByText('Cliente Alfa').click();
  await page.getByRole('button', { name: 'Transferir conversa' }).click();
  const dialog = page.getByRole('dialog', { name: 'Transferir conversa' });
  await expect(dialog.getByLabel('Beto')).toBeVisible();
  await expect(dialog.getByText('2 abertas')).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Transferir' })).toBeDisabled();
  await dialog.getByLabel('Beto').check();
  await page.screenshot({ path: info.outputPath('hub-transfer.png') });
  await dialog.getByRole('button', { name: 'Transferir' }).click();
  await expect(dialog).toHaveCount(0);
  expect(writes.find((w) => w.path.endsWith('/transfer'))!.body).toEqual({ expected_tenant_id: 'A', to_user_id: 'u-beto' });
});
