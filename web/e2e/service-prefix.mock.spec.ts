import { test, expect, Page, Route } from '@playwright/test';

// Tela de Configurações com o prefixo do protocolo (ADR-0049) em um navegador real, com a API MOCKADA. O backend é provado por testes Go contra PostgreSQL real;
// aqui se confere o que a pessoa vê: quem pode escolher, a sugestão, o próximo número, o travamento depois do primeiro protocolo.

const TENANT = '11111111-1111-1111-1111-111111111111';

async function install(page: Page, opts: { manage: boolean; prefix: { prefix: string; configured: boolean; locked: boolean; next_protocol: string } | 'absent' }) {
  const puts: { prefix: string }[] = [];
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.addInitScript(([t]) => {
    localStorage.setItem('token', 'mock-token');
    localStorage.setItem('tenantId', t);
    localStorage.setItem('sessionActive', 'true');
    localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'keslley@k3g.example', name: 'Keslley' }));
  }, [TENANT]);
  await page.route('**/api/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname.replace('/api/v1', '');
    const method = route.request().method();
    if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
    if (p.endsWith('/me/access')) return json(route, { role_key: 'x', permissions: opts.manage ? ['tenant.read', 'tenant.manage'] : ['tenant.read'] });
    if (p.endsWith('/settings/inbox')) return json(route, { wait_warn_minutes: 30, wait_danger_minutes: 120 });
    if (p.endsWith('/integrations/ai')) return route.fulfill({ status: 404, body: '' });
    if (p.endsWith('/service-settings/protocol-prefix')) {
      if (opts.prefix === 'absent') return route.fulfill({ status: 404, body: '' });
      if (method === 'PUT') {
        const body = route.request().postDataJSON();
        puts.push(body);
        if (body.prefix === 'ABC') return json(route, { error: 'prefix_taken' }, 409);
        return json(route, { prefix: body.prefix, configured: true, locked: false, next_protocol: `${body.prefix}-1001` });
      }
      return json(route, opts.prefix);
    }
    return route.fulfill({ status: 404, body: '' });
  });
  return { puts };
}

test.describe('Prefixo do protocolo (Configurações)', () => {
  test('o administrador vê a sugestão, escolhe outro prefixo e vê o próximo número', async ({ page }) => {
    const api = await install(page, { manage: true, prefix: { prefix: 'KTG', configured: false, locked: false, next_protocol: 'KTG-1001' } });
    await page.goto('/settings/general');
    const card = page.getByRole('heading', { name: 'Prefixo do protocolo de atendimento' });
    await expect(card).toBeVisible({ timeout: 15_000 });
    const input = page.getByLabel('Prefixo');
    await expect(input).toHaveValue('KTG');
    await input.fill('');
    await input.pressSequentially('k3x9z');
    await expect(input).toHaveValue('KXZ'); // só letras, em maiúsculas
    await expect(page.getByText('KXZ-1001')).toBeVisible();
    await page.getByRole('button', { name: 'Reservar prefixo' }).click();
    await expect(page.getByText(/Prefixo KXZ reservado/)).toBeVisible();
    expect(api.puts).toEqual([{ prefix: 'KXZ' }]);
  });

  test('um prefixo de outra empresa é explicado, e uma letra só não deixa salvar', async ({ page }) => {
    await install(page, { manage: true, prefix: { prefix: 'KTG', configured: false, locked: false, next_protocol: 'KTG-1001' } });
    await page.goto('/settings/general');
    const input = page.getByLabel('Prefixo');
    await expect(input).toBeVisible({ timeout: 15_000 });
    await input.fill('K');
    await expect(page.getByRole('button', { name: 'Reservar prefixo' })).toBeDisabled();
    await input.fill('ABC');
    await page.getByRole('button', { name: 'Reservar prefixo' }).click();
    await expect(page.getByText('Outra empresa já usa este prefixo. Escolha outro.')).toBeVisible();
  });

  test('depois do primeiro protocolo o prefixo está travado: sem botão e sem edição', async ({ page }) => {
    await install(page, { manage: true, prefix: { prefix: 'KTG', configured: true, locked: true, next_protocol: 'KTG-1006' } });
    await page.goto('/settings/general');
    const input = page.getByLabel('Prefixo');
    await expect(input).toBeDisabled({ timeout: 15_000 });
    await expect(input).toHaveValue('KTG');
    await expect(page.getByRole('button', { name: 'Reservar prefixo' })).toHaveCount(0);
  });

  test('quem não administra não vê o cartão, e com o módulo desligado ele não existe', async ({ page }) => {
    await install(page, { manage: false, prefix: { prefix: 'KTG', configured: false, locked: false, next_protocol: 'KTG-1001' } });
    await page.goto('/settings/general');
    await expect(page.getByText('Tempo de espera na lista de conversas')).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('heading', { name: 'Prefixo do protocolo de atendimento' })).toHaveCount(0);
  });

  test('com o módulo de serviço desligado (404) o cartão some', async ({ page }) => {
    await install(page, { manage: true, prefix: 'absent' });
    await page.goto('/settings/general');
    await expect(page.getByText('Tempo de espera na lista de conversas')).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('heading', { name: 'Prefixo do protocolo de atendimento' })).toHaveCount(0, { timeout: 10_000 });
  });
});
