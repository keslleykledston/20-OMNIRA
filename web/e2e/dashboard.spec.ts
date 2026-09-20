import { test, expect } from '@playwright/test';

test.describe('Dashboard', () => {
  test.beforeEach(async ({ page }) => {
    // Login antes de cada teste
    await page.goto('/login');
    await page.fill('input[placeholder="seu@email.com"]', 'test@omnira.local');
    await page.fill('input[type="password"]', 'pass');
    await page.click('button:has-text("Entrar")');
    await page.waitForURL('/');
  });

  test('exibir KPI cards', async ({ page }) => {
    // Verificar cards
    await expect(page.locator('text=Total de Contas')).toBeVisible();
    await expect(page.locator('text=Tickets Abertos')).toBeVisible();
    await expect(page.locator('text=Conformidade SLA')).toBeVisible();
    await expect(page.locator('text=Alertas Ativos')).toBeVisible();
  });

  test('exibir atividades recentes', async ({ page }) => {
    // Verificar seção de atividades
    await expect(page.locator('text=Atividade Recente')).toBeVisible();

    // Verificar se tem atividades listadas
    const activities = page.locator('[class*="flex items-center gap-4"]');
    const count = await activities.count();
    expect(count).toBeGreaterThan(0);
  });

  test('exibir resumo do usuário', async ({ page }) => {
    // Verificar bem-vindo
    await expect(page.locator('text=Bem-vindo')).toBeVisible();

    // Verificar info do usuário
    await expect(page.locator('text=test@omnira.local')).toBeVisible();
  });

  test('navegar para outras páginas', async ({ page }) => {
    // Clique em Contas no sidebar
    await page.click('a:has-text("Contatos")');
    await page.waitForURL('/accounts');

    // Verificar que está em Contas
    await expect(page.locator('text=Contas BPO')).toBeVisible();
  });
});
