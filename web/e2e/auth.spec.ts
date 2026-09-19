import { test, expect } from '@playwright/test';

test.describe('Authentication', () => {
  test('login com email válido', async ({ page }) => {
    await page.goto('/login');

    // Verificar elementos da página
    await expect(page.locator('text=OMNIRA')).toBeVisible();
    await expect(page.locator('text=Plataforma SaaS Empresarial')).toBeVisible();

    // Preencher formulário
    await page.fill('input[placeholder="seu@email.com"]', 'test@omnira.local');
    await page.fill('input[type="password"]', 'password123');

    // Clicar em Entrar
    await page.click('button:has-text("Entrar")');

    // Aguardar redirecionamento
    await page.waitForURL('/', { timeout: 5000 });

    // Verificar se está no dashboard
    await expect(page.locator('text=Dashboard')).toBeVisible({ timeout: 3000 });
  });

  test('rejeitar email inválido', async ({ page }) => {
    await page.goto('/login');

    // Preencher com email inválido
    await page.fill('input[placeholder="seu@email.com"]', 'invalid@email.com');
    await page.fill('input[type="password"]', 'password');

    // Clicar em Entrar
    await page.click('button:has-text("Entrar")');

    // Verificar mensagem de erro
    await expect(page.locator('text=Email não encontrado')).toBeVisible({ timeout: 2000 });
  });

  test('logout funciona', async ({ page }) => {
    // Login primeiro
    await page.goto('/login');
    await page.fill('input[placeholder="seu@email.com"]', 'test@omnira.local');
    await page.fill('input[type="password"]', 'pass');
    await page.click('button:has-text("Entrar")');
    await page.waitForURL('/');

    // Clicar em Sair
    await page.click('button:has-text("Sair")');

    // Verificar redirecionamento
    await page.waitForURL('/login');
    await expect(page.locator('text=OMNIRA')).toBeVisible();
  });
});
