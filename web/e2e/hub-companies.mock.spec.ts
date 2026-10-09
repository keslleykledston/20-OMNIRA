import { test, expect, Page, Route } from '@playwright/test';

// Aba "Instâncias" do painel de Acessos (plano de controle, ADR-0038 + ADR-0039) em um navegador real, com a API mockada. O backend é provado por testes Go
// contra PostgreSQL real; aqui se confere a tela: só aparece para quem pode gerenciar, lista, criar, suspender e ligar/desligar capacidades.

const TENANT = '11111111-1111-1111-1111-111111111111';
const HUB = '99999999-0000-0000-0000-000000000001';
const CAPS = [
  { key: 'whatsapp_channel', label: 'Canal WhatsApp', description: 'Conectar linhas de WhatsApp.', gates: 'Criar nova conexão de WhatsApp.' },
  { key: 'erp_crm', label: 'ERP / CRM', description: 'Integrar retaguarda.', gates: 'Criar nova integração.' },
  { key: 'outbound_attachments', label: 'Anexos de saída', description: 'Enviar arquivos.', gates: 'Enviar anexos.' },
];
const co = (id: string, name: string, over: Record<string, unknown> = {}) => ({
  id, legal_name: `${name} Ltda`, trade_name: name, display_name: name, status: 'active', contract_status: 'active',
  capabilities: { whatsapp_channel: true, erp_crm: true, outbound_attachments: true }, channels: 1, integrations: 1, open_conversations: 4, agents: 2,
  created_at: new Date().toISOString(), ...over,
});

async function install(page: Page, opts: { manage: boolean }) {
  const companies = [co('c1', 'ISP Roraima'), co('c2', 'NorteNet', { status: 'suspended' })];
  const writes: { method: string; path: string; body: any; key?: string }[] = [];
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
    if (p === '/hubs') return json(route, { items: [{ id: HUB, name: 'K3G Solutions', role: opts.manage ? 'hub_admin' : 'hub_agent', can_manage_access: opts.manage, can_manage_companies: opts.manage }] });
    if (p === `/hubs/${HUB}/inbox`) return json(route, { items: [], has_more: false, count: 0, limit: 30 });
    if (p === `/hubs/${HUB}/access`) {
      const person = (id: string, email: string) => ({ user_id: id, name: '', email });
      return json(route, { hub_id: HUB, agents: [], invitations: [], instances: companies.map((c: any) => ({ tenant_id: c.id, name: c.display_name, tenant_status: c.status, contract_status: c.contract_status, admins: [person('adm-' + c.id, `adm@${c.id}.example`)], direct_agents: 1, hub_agents: 0 })) });
    }
    if (p === `/hubs/${HUB}/companies` && method === 'GET') return json(route, { items: companies, capabilities: CAPS });
    if (p === `/hubs/${HUB}/companies` && method === 'POST') {
      const body = route.request().postDataJSON();
      writes.push({ method, path: p, body, key: route.request().headers()['idempotency-key'] });
      const created = co('c3', body.trade_name || body.legal_name);
      companies.push(created);
      return json(route, created, 201);
    }
    const m = p.match(new RegExp(`^/hubs/${HUB}/companies/([^/]+)$`));
    if (m && method === 'PATCH') {
      const body = route.request().postDataJSON();
      writes.push({ method, path: p, body });
      const c = companies.find((x) => x.id === m[1]) as any;
      if (!c) return json(route, {}, 404);
      if (body.status) c.status = body.status;
      if (body.capabilities) c.capabilities = { ...c.capabilities, ...body.capabilities };
      return json(route, c);
    }
    if (p === '/tenants') return json(route, [{ id: TENANT, legal_name: 'K3G' }]);
    if (p.endsWith('/me/access')) return json(route, { permissions: [], role_key: 'tenant_admin' });
    return json(route, {}, 404);
  });
  return writes;
}

test('o operador vê as instâncias, cria uma, suspende e liga/desliga capacidades', async ({ page }) => {
  const writes = await install(page, { manage: true });
  // o endereço antigo /hub/empresas leva à aba; não existe mais uma tela separada
  await page.goto('/hub/empresas');
  await expect(page).toHaveURL(/\/acessos\?aba=instancias$/);
  await expect(page.getByRole('tab', { name: 'Instâncias', selected: true })).toBeVisible();

  const a = page.getByRole('region', { name: 'Instância ISP Roraima' });
  await expect(a.getByText('Ativa')).toBeVisible();
  await expect(a.getByText('adm@c1.example')).toBeVisible(); // os administradores ficam no MESMO cartão da instância
  await expect(page.getByRole('region', { name: 'Instância NorteNet' }).getByText('Suspensa')).toBeVisible();

  // a chave só muda quando o servidor confirma (o servidor é a verdade), então clica e espera o estado novo
  await a.getByLabel(/ERP \/ CRM/).click();
  await expect(a.getByLabel(/ERP \/ CRM/)).not.toBeChecked();
  await expect.poll(() => writes.some((w) => w.method === 'PATCH' && w.body.capabilities?.erp_crm === false)).toBe(true);

  await a.getByRole('button', { name: 'Suspender' }).click();
  await expect(page.getByText(/perdem o acesso na hora/)).toBeVisible();
  await page.getByRole('button', { name: 'Suspender instância' }).click();
  await expect(a.getByText('Suspensa')).toBeVisible();

  await page.getByRole('button', { name: 'Nova instância' }).click();
  const dialog = page.getByRole('dialog', { name: 'Nova instância' });
  await dialog.getByLabel(/Razão social/).fill('Fibra Norte Ltda');
  await dialog.getByLabel(/Nome fantasia/).fill('Fibra Norte');
  await dialog.getByRole('button', { name: 'Criar instância' }).click();
  await expect(page.getByText(/Ninguém tem acesso a ela ainda/)).toBeVisible();
  await expect(page.getByRole('region', { name: 'Instância Fibra Norte' })).toBeVisible();
  const post = writes.find((w) => w.method === 'POST')!;
  expect(post.key).toMatch(/^new-company-/);
  expect(post.body.legal_name).toBe('Fibra Norte Ltda');
});

test('quem não administra o Hub não vê o item Acessos e a página diz que está indisponível', async ({ page }) => {
  await install(page, { manage: false });
  await page.goto('/inbox');
  await expect(page.getByRole('link', { name: 'Acessos' })).toHaveCount(0);
  await page.goto('/acessos');
  await expect(page.getByText('Painel de acessos indisponível')).toBeVisible();
});
