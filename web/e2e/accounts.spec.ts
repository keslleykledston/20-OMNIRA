import { test, expect } from '@playwright/test';

test.describe('Contas', () => {
  test.beforeEach(async ({ page }) => {
    // Login
    await page.goto('/login');
    await page.fill('input[placeholder="seu@email.com"]', 'test@omnira.local');
    await page.fill('input[type="password"]', 'pass');
    await page.click('button:has-text("Entrar")');

    // Ir para Contas
    await page.goto('/accounts');
  });

  test('listar contas', async ({ page }) => {
    // Aguardar tabela
    await expect(page.locator('table')).toBeVisible({ timeout: 3000 });

    // Verificar contas aparecem
    await expect(page.locator('text=Test Company LTDA')).toBeVisible();
    await expect(page.locator('text=Support Center SP')).toBeVisible();
  });

  test('filtrar por status', async ({ page }) => {
    // Clique em "Ativas"
    await page.click('button:has-text("Ativas")');

    // Verificar filtro aplicado
    await expect(page.locator('text=Ativas (3)')).toBeVisible();
  });

  test('criar nova conta', async ({ page }) => {
    // Clique em "+ Nova Conta"
    await page.click('button:has-text("Nova Conta")');

    // Verificar formulário apareceu
    await expect(page.locator('text=Criar Nova Conta')).toBeVisible();

    // Preencher formulário
    await page.fill('input[type="text"]', 'Test Account New');

    // Selecionar tipo
    await page.selectOption('select', 'operator');

    // Clicar em Criar
    await page.click('button:has-text("Criar Conta")');

    // Aguardar modal fechar
    await page.waitForTimeout(500);

    // Verificar tabela atualizada
    await expect(page.locator('table')).toBeVisible();
  });

  test('ver detalhes da conta', async ({ page }) => {
    // Clique em "Ver" em uma conta
    await page.click('button:has-text("Ver")', { nth: 0 });

    // Verificar modal abriu com detalhes
    await expect(page.locator('.fixed')).toBeVisible();
    await page.waitForTimeout(500);
  });

  test('suspender conta', async ({ page }) => {
    // Clique em "Suspender" em uma conta ativa
    await page.click('button:has-text("Suspender")', { nth: 0 });

    // Aguardar atualização
    await page.waitForTimeout(1000);

    // Verificar que agora mostra "Ativar" em vez de "Suspender"
    const buttons = page.locator('button:has-text("Ativar")');
    expect(await buttons.count()).toBeGreaterThan(0);
  });
});
