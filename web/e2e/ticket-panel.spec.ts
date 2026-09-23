import { test, expect, Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';

const DB = process.env.E2E_DB || 'omnira_e2e';
const PG = process.env.E2E_PG_CONTAINER || 'omnira-postgres';
const TENANT = '11111111-1111-1111-1111-111111111111';
const CONV_WITH_CRM = 'd0d0d0d0-0000-0000-0000-000000000002';
const CRM_CONTACT = { id: 'c0c0c0c0-0000-0000-0000-0000000000c2', name: 'Carla CRM', phone: '+5511955554444' };
const CRM_CONTACT_ID = 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee';
const AGENT = { email: 'test@omnira.local' };

function sql(query: string): string {
  return execFileSync('docker', ['exec', PG, 'psql', '-U', 'omnira', '-d', DB, '-tA', '-c', query], { encoding: 'utf8' }).trim();
}

async function login(page: Page, email: string) {
  await page.goto('/login');
  await page.getByLabel('E-mail').fill(email);
  await page.getByRole('button', { name: /Entrar/ }).click();
  await page.waitForURL('/inbox', { timeout: 10_000 });
}

// The inbox workspace lists conversations as h4 rows; the context pane hosts the TicketPanel.
async function openConversation(page: Page, contactName: string) {
  await page.goto('/inbox');
  const row = page.getByRole('heading', { level: 4, name: contactName });
  await expect(row).toBeVisible();
  await row.click();
  await expect(page.getByRole('heading', { name: 'Chamado' })).toBeVisible({ timeout: 10_000 });
}

test.describe('TicketPanel — CRM.5: operador humano', () => {
  test('operador: cria, atualiza status e fecha ticket via UI (fluxo completo)', async ({ page }) => {
    await login(page, AGENT.email);
    await openConversation(page, 'Maria Souza');

    await page.getByPlaceholder('Novo chamado...').fill('Solicitação de mudança de plano');
    const open = page.getByRole('button', { name: 'Abrir', exact: true });
    await expect(open).toBeEnabled();
    await open.click();

    await expect(page.getByText('Solicitação de mudança de plano')).toBeVisible();
    await expect(page.getByText('open', { exact: true })).toBeVisible();

    await page.getByRole('button', { name: 'Trabalhando' }).click();
    await expect(page.getByText('in_progress', { exact: true })).toBeVisible();

    await page.getByRole('button', { name: 'Resolvido' }).click();
    await expect(page.getByText('resolved', { exact: true })).toBeVisible();

    await page.getByRole('button', { name: 'Fechar', exact: true }).click();
    await expect(page.getByText('closed', { exact: true })).toBeVisible();

    await expect(page.getByRole('alert')).toHaveCount(0);
  });

  test('R5.2: painel de atividade CRM aparece e reflete a ausência de empresas (sem CRM configurado)', async ({ page }) => {
    // Conversation whose contact is linked to a CRM contact (its own contact keeps the list row unambiguous).
    sql(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164)
         VALUES ('${CRM_CONTACT.id}','${TENANT}','${CRM_CONTACT.name}','${CRM_CONTACT.phone}') ON CONFLICT DO NOTHING`);
    sql(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,crm_contact_id,status)
         VALUES ('${CONV_WITH_CRM}','${TENANT}','${CRM_CONTACT.id}','c0000000-0000-0000-0000-00000000c001','${CRM_CONTACT_ID}','open')
         ON CONFLICT DO NOTHING`);

    await login(page, AGENT.email);
    await openConversation(page, CRM_CONTACT.name);

    await expect(page.getByRole('heading', { name: 'Atividade CRM' })).toBeVisible({ timeout: 5000 });
    await expect(page.locator('select').first()).toBeVisible();

    const subject = page.getByPlaceholder('Descrição da atividade...');
    await expect(subject).toBeVisible();
    await subject.fill('Suporte técnico para integração de API');

    // The e2e stack has no K3G CRM, so the company list is empty: the panel must say so
    // and keep the action disabled instead of pretending an activity can be created.
    await expect(page.getByLabel('Empresa')).toBeDisabled();
    await expect(page.getByText('Nenhuma empresa disponível')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Criar Atividade', exact: true })).toBeDisabled();
  });
});
