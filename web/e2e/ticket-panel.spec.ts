import { test, expect, Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';

const DB = process.env.E2E_DB || 'omnira_e2e';
const PG = process.env.E2E_PG_CONTAINER || 'omnira-postgres';
const TENANT = '11111111-1111-1111-1111-111111111111';
const CONV = 'd0d0d0d0-0000-0000-0000-000000000001';
const AGENT = { email: 'test@omnira.local', id: '22222222-2222-2222-2222-222222222222' };

function sql(query: string): string {
  return execFileSync('docker', ['exec', PG, 'psql', '-U', 'omnira', '-d', DB, '-tA', '-c', query], { encoding: 'utf8' }).trim();
}

async function login(page: Page, email: string) {
  await page.goto('/login');
  await page.fill('input[placeholder="seu@email.com"]', email);
  await page.fill('input[type="password"]', 'irrelevant');
  await page.click('button:has-text("Entrar")');
  await page.waitForURL('/', { timeout: 10_000 });
}

test.describe('TicketPanel — CRM.5: operador humano', () => {
  test('operador: cria, atualiza status e fecha ticket via UI (fluxo completo)', async ({
    page,
  }) => {

    // Step 1: Login
    await login(page, AGENT.email);

    // Step 2: Navigate to conversation
    await page.goto(`/inbox/conversations/${CONV}?tenant=${TENANT}`);
    await page.waitForSelector('.conversation-page', { timeout: 10_000 });

    // Step 3: Validar que TicketPanel está visível
    const ticketPanel = page.locator('text=Chamado').first();
    await expect(ticketPanel).toBeVisible({ timeout: 5000 });

    // Step 4: Criar ticket com assunto
    const subjectInput = page.locator('input[placeholder="Novo chamado..."]');
    const openButton = page.locator('button').filter({ hasText: /^Abrir$/ }).first();

    await subjectInput.fill('Solicitação de mudança de plano');
    await expect(openButton).toBeEnabled();
    await openButton.click();

    // Aguardar criação (botão "Trabalhando" aparece)
    await page.waitForSelector('button:has-text("Trabalhando")', { timeout: 5000 });

    // Validar que ticket foi criado
    await expect(page.locator('text=Solicitação de mudança de plano')).toBeVisible();
    await expect(page.locator('text=open')).toBeVisible();

    // Step 5: Atualizar status para "in_progress"
    const workingButton = page.locator('button:has-text("Trabalhando")').first();
    await workingButton.click();

    // Aguardar atualização
    await page.waitForSelector('text=in_progress', { timeout: 5000 });
    await expect(page.locator('text=in_progress')).toBeVisible();

    // Step 6: Atualizar status para "resolved"
    const resolvedButton = page.locator('button:has-text("Resolvido")').first();
    await resolvedButton.click();

    // Aguardar atualização
    await page.waitForSelector('text=resolved', { timeout: 5000 });
    await expect(page.locator('text=resolved')).toBeVisible();

    // Step 7: Fechar ticket
    const closeButton = page.locator('button:has-text("Fechar")').first();
    await closeButton.click();

    // Aguardar fechamento
    await page.waitForSelector('text=closed', { timeout: 5000 });
    await expect(page.locator('text=closed')).toBeVisible();

    // Step 8: Validar que não há erros na página
    const errorMessages = page.locator('[role="alert"]');
    const errorCount = await errorMessages.count();
    expect(errorCount).toBe(0);
  });
});
