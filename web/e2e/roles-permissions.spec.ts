import { test, expect, Page } from '@playwright/test';

// Real stack (scripts/e2e-inbox.sh). Roles are fixed by the platform: the screen is read-only.
const ADMIN = 'admin@omnira.local';
const AGENT = 'test@omnira.local';

async function login(page: Page, email: string) {
  await page.goto('/login');
  await page.getByLabel('E-mail').fill(email);
  await page.getByRole('button', { name: /Entrar/ }).click();
  await page.waitForURL('/', { timeout: 10_000 });
}

test('admin reaches the read-only role matrix from Equipe e acesso', async ({ page }) => {
  await login(page, ADMIN);
  await page.goto('/settings/team');
  await page.getByRole('link', { name: 'Funções e permissões' }).click();
  await expect(page).toHaveURL(/\/settings\/roles$/);

  await expect(page.getByRole('tab')).toHaveText(['Administrador', 'Supervisor', 'Agente']);
  await expect(page.getByRole('tab', { name: 'Administrador' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByText('Seu papel')).toBeVisible();
  await expect(page.getByText('As permissões deste papel são definidas pela plataforma.')).toBeVisible();

  const row = (label: string) => page.getByRole('listitem').filter({ hasText: label });
  await expect(row('Gerenciar canais')).toContainText('Permitido');

  await page.getByRole('tab', { name: 'Agente' }).click();
  await expect(row('Assumir e responder conversas')).toContainText('Permitido');
  await expect(row('Gerenciar canais')).toContainText('Não permitido');
  await expect(row('Gerenciar equipe')).toContainText('Não permitido');

  // Nothing editable on this screen.
  for (const role of ['checkbox', 'switch', 'textbox', 'combobox', 'radio']) {
    await expect(page.getByRole(role as 'checkbox')).toHaveCount(0);
  }
});

test('an agent without membership.read is told they have no access', async ({ page }) => {
  await login(page, AGENT);
  await page.goto('/settings/roles');
  await expect(page.getByText('Você não tem permissão para ver as funções e permissões.')).toBeVisible();
  await expect(page.getByRole('tab')).toHaveCount(0);
});
