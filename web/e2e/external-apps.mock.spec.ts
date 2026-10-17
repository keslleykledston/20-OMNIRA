import { test, expect, Page, Route } from '@playwright/test';

// Aplicações externas (ADR-0048) em um navegador real, API MOCKADA: o painel lateral "Nova aplicação" tem título visível e margem interna
// (antes o conteúdo encostava nas bordas e desorganizava a tela).

const TENANT = '11111111-1111-1111-1111-111111111111';

async function install(page: Page) {
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.addInitScript(([t]) => {
    localStorage.setItem('token', 'mock-token');
    localStorage.setItem('tenantId', t);
    localStorage.setItem('sessionActive', 'true');
    localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'keslley@k3g.example', name: 'Keslley' }));
  }, [TENANT]);
  await page.route('**/api/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname.replace('/api/v1', '');
    if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
    if (p.endsWith('/me/access')) return json(route, { role_key: 'tenant_admin', permissions: ['integration.read', 'integration.manage', 'tenant.read'] });
    if (p.endsWith('/integrations/apps')) return json(route, { items: [], count: 0 });
    if (p.endsWith('/queues')) return json(route, { items: [{ id: 'q1', name: 'Default', mode: 'manual', is_default: true }, { id: 'q2', name: 'Suporte', mode: 'manual', is_default: false }] });
    if (p.endsWith('/channels/connections') || p.endsWith('/connections')) return json(route, { items: [{ id: 'c1', provider: 'waha', external_account_id: '559291882864' }] });
    return route.fulfill({ status: 404, body: '' });
  });
}

test('o painel "Nova aplicação" tem título e respiro nas bordas', async ({ page }) => {
  await install(page);
  await page.goto('/settings/integrations/apps');
  await page.getByRole('button', { name: /Nova aplicação/ }).first().click();
  const panel = page.getByRole('dialog', { name: 'Nova aplicação externa' });
  await expect(panel).toBeVisible({ timeout: 15_000 });
  await expect(panel.getByRole('heading', { name: 'Nova aplicação externa' })).toBeVisible();
  const box = await panel.boundingBox();
  const input = await panel.getByLabel('Nome da aplicação').boundingBox();
  expect(box && input && input.x - box.x >= 20).toBeTruthy(); // margem interna à esquerda
  expect(box && input && box.x + box.width - (input.x + input.width) >= 20).toBeTruthy(); // e à direita
  await page.screenshot({ path: '/data/cafegpt/tmp/pw-results/external-apps-drawer.png' });
});
