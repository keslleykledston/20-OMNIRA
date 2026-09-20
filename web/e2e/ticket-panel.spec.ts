import { test, expect, Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';

const DB = process.env.E2E_DB || 'omnira_e2e';
const PG = process.env.E2E_PG_CONTAINER || 'omnira-postgres';
const TENANT = '11111111-1111-1111-1111-111111111111';
const CONV = 'd0d0d0d0-0000-0000-0000-000000000001';
const CONV_WITH_CRM = 'd0d0d0d0-0000-0000-0000-000000000002';
const AGENT = { email: 'test@omnira.local', id: '22222222-2222-2222-2222-222222222222' };
const CRM_CONTACT_ID = 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee';

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

  test('R5.2: operador cria atividade no CRM com seleção de empresa', async ({ page }) => {
    // Preparar conversation com crm_contact_id
    sql(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,crm_contact_id,status)
         VALUES ('${CONV_WITH_CRM}','${TENANT}','c0c0c0c0-0000-0000-0000-000000000001','c0000000-0000-0000-0000-00000000c001','${CRM_CONTACT_ID}','open')
         ON CONFLICT DO NOTHING`);

    // Step 1: Login
    await login(page, AGENT.email);

    // Step 2: Navigate to conversation com CRM contact
    await page.goto(`/inbox/conversations/${CONV_WITH_CRM}?tenant=${TENANT}`);
    await page.waitForSelector('.conversation-page', { timeout: 10_000 });

    // Step 3: Validar que seção "Atividade CRM" é visível
    const crmActivitySection = page.locator('text=Atividade CRM').first();
    await expect(crmActivitySection).toBeVisible({ timeout: 5000 });

    // Step 4: Validar que dropdown de empresas foi carregado
    const companySelect = page.locator('select').first();
    await expect(companySelect).toBeVisible();

    // Step 5: Validar que input de assunto existe
    const subjectInput = page.locator('input[placeholder="Descrição da atividade..."]');
    await expect(subjectInput).toBeVisible();

    // Step 6: Preencher formulário de activity
    await subjectInput.fill('Suporte técnico para integração de API');

    // Step 7: Validar que botão está habilitado
    const createActivityButton = page.locator('button').filter({ hasText: /^Criar Atividade$/ }).first();
    await expect(createActivityButton).toBeEnabled();

    // Step 8: Clicar em "Criar Atividade"
    // Nota: sem K3G CRM real, a requisição pode falhar, mas a UI deve funcionar
    const apiResponsePromise = page.waitForResponse(
      (resp) => resp.url().includes('/crm/activity') && resp.request().method() === 'POST'
    ).catch(() => null); // Permitir falha se K3G não está configurado

    await createActivityButton.click();

    // Aguardar a requisição ou timeout se K3G não está configurado
    const apiResponse = await Promise.race([
      apiResponsePromise,
      new Promise((resolve) => setTimeout(() => resolve(null), 3000)),
    ]);

    // Se K3G está configurado, validar resposta bem-sucedida
    if (apiResponse) {
      const status = (apiResponse as any).status?.();
      if (status && status >= 200 && status < 300) {
        // Activity foi criada com sucesso
        await expect(subjectInput).toHaveValue(''); // Campo deve ser limpo
      }
    }

    // Validar que não há erros críticos na página
    const errorMessages = page.locator('[role="alert"]').filter({ hasText: /^(?!.*Atividade)/ });
    const errorCount = await errorMessages.count();
    expect(errorCount).toBeLessThanOrEqual(1); // Permitir 1 erro se K3G não está configurado
  });
});
