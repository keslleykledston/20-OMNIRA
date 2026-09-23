import { test, expect, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'

// PRODUCT.2-B: /tickets is now a REAL canonical listing (GET .../tickets,
// ticket.read-gated, same tickets table TicketPanel already uses for its
// per-conversation ticket — see ticket-panel.spec.ts). No mock ticket data.
const DB = process.env.E2E_DB || 'omnira_e2e'
const PG = process.env.E2E_PG_CONTAINER || 'omnira-postgres'
const CONV = 'd0d0d0d0-0000-0000-0000-000000000001'
const PROJECTED_CONV = 'd0d0d0d0-0000-0000-0000-000000000003'
const PROJECTED_CONTACT = 'c0c0c0c0-0000-0000-0000-000000000003'
const ADMIN = 'admin@omnira.local'
const AGENT = 'test@omnira.local'

function sql(query: string): string {
  return execFileSync('docker', ['exec', PG, 'psql', '-U', 'omnira', '-d', DB, '-tA', '-c', query], { encoding: 'utf8' }).trim()
}

async function login(page: Page, email: string) {
  await page.goto('/login')
  await page.getByLabel('E-mail').fill(email)
  await page.getByRole('button', { name: /Entrar/ }).click()
  await page.waitForURL('/inbox', { timeout: 10_000 })
}

// Seeded directly against the real tickets table (same fixture mechanism
// e2e/fixtures.sql and other real-stack specs use), tied to the existing
// fixture conversation — not a mock/fake API response.
const SEEDED_SUBJECT = 'E2E canonical ticket listing'
// PRODUCT.6-D (ADR-0013): a second, distinct ticket with the external ERP
// projection columns populated — never a real K3G request, purely a fixture
// row, exactly like PRODUCT.6-B's SetCRMConnector test-only injection
// pattern. Proves /tickets and CSV export render real projection metadata
// without inventing a connector or calling K3G.
const PROJECTED_SUBJECT = 'E2E external-backed ticket'
test.beforeAll(() => {
  sql(`INSERT INTO tickets (id, tenant_id, conversation_id, status, priority, subject, created_at, updated_at)
       SELECT '99999999-0000-0000-0000-000000000001', tenant_id, id, 'open', 'high', '${SEEDED_SUBJECT}', now(), now()
       FROM conversations WHERE id='${CONV}'
       ON CONFLICT (id) DO UPDATE SET status='open', updated_at=now()`)
  sql(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, status)
       SELECT '${PROJECTED_CONTACT}', tenant_id, 'E2E Projected Contact', '+5511955559999', 'active'
       FROM conversations WHERE id='${CONV}'
       ON CONFLICT (id) DO NOTHING`)
  sql(`INSERT INTO conversations (id, tenant_id, contact_id, channel_connection_id, status)
       SELECT '${PROJECTED_CONV}', tenant_id, '${PROJECTED_CONTACT}', channel_connection_id, 'open'
       FROM conversations WHERE id='${CONV}'
       ON CONFLICT (id) DO NOTHING`)
  sql(`INSERT INTO tickets (id, tenant_id, conversation_id, status, priority, subject, created_at, updated_at,
         provider, external_ticket_id, external_status, external_status_label, sync_status, last_synced_at)
       SELECT '99999999-0000-0000-0000-000000000002', tenant_id, id, 'open', 'high', '${PROJECTED_SUBJECT}', now(), now(),
         'k3g_crm', '28176', '1', 'Novo', 'synced', now()
       FROM conversations WHERE id='${PROJECTED_CONV}'
       ON CONFLICT (id) DO UPDATE SET status='open', updated_at=now()`)
})
test.afterAll(() => {
  sql(`DELETE FROM tickets WHERE id IN ('99999999-0000-0000-0000-000000000001','99999999-0000-0000-0000-000000000002')`)
  sql(`DELETE FROM conversations WHERE id='${PROJECTED_CONV}'`)
  sql(`DELETE FROM contacts WHERE id='${PROJECTED_CONTACT}'`)
})

test('admin sees the real seeded ticket with real status and priority', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/tickets')
  await expect(page.getByRole('heading', { name: 'Tickets' })).toBeVisible()

  // Desktop table + mobile card fallback both exist in the DOM (CSS-hidden by
  // breakpoint, not unmounted), so scope to the table row, the same way
  // agents.spec.ts/presence.spec.ts already do.
  const row = page.getByRole('row', { name: new RegExp(SEEDED_SUBJECT) })
  await expect(row).toBeVisible()
  await expect(row).toContainText('Alta')
  await expect(row).toContainText('Aberto')

  // No mock ticket content from the retired fixture (e.g. legacy "TKT-001" ids).
  await expect(page.getByText(/TKT-\d/)).toHaveCount(0)
})

test('a user without ticket.read is denied, not shown a fabricated list', async ({ page }) => {
  await login(page, AGENT)
  await page.goto('/tickets')
  await expect(page.getByText('Você não tem permissão para visualizar os tickets deste tenant.')).toBeVisible()
  await expect(page.getByRole('table')).toHaveCount(0)
})

test('status filter narrows the real list via the real API', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/tickets')
  const row = page.getByRole('row', { name: new RegExp(SEEDED_SUBJECT) })
  await expect(row).toBeVisible()

  await page.getByLabel('Filtrar por status').selectOption('closed')
  await expect(row).toHaveCount(0)

  await page.getByLabel('Filtrar por status').selectOption('open')
  await expect(row).toBeVisible()
})

// PRODUCT.5-A: real CSV export — GET .../tickets/export.csv, same
// ticket.read gate, real Postgres-backed rows, real browser download.
test('admin exports the real filtered ticket list as a real CSV download', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/tickets')
  await expect(page.getByRole('row', { name: new RegExp(SEEDED_SUBJECT) })).toBeVisible()

  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('button', { name: /Exportar CSV/ }).click()
  const download = await downloadPromise

  expect(download.suggestedFilename()).toBe('tickets.csv')
  const path = await download.path()
  const fs = await import('node:fs')
  const content = fs.readFileSync(path!, 'utf8')

  expect(content).toContain('id,conversation_id,subject,status,priority,assigned_to,created_at,updated_at')
  expect(content).toContain(SEEDED_SUBJECT)
  expect(content).not.toMatch(/TKT-\d/)
})

test('a user without ticket.read cannot see or trigger the export action', async ({ page }) => {
  await login(page, AGENT)
  await page.goto('/tickets')
  await expect(page.getByRole('button', { name: /Exportar CSV/ })).toHaveCount(0)
})

// PRODUCT.6-D (ADR-0013): the legacy ticket and the external-projected
// ticket must both be visible, and their origin distinguishable — never a
// fake connector wired in, purely real Postgres rows.
test('legacy ticket shows "Local", external-projected ticket shows its provider and ID', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/tickets')

  const legacyRow = page.getByRole('row', { name: new RegExp(SEEDED_SUBJECT) })
  await expect(legacyRow).toBeVisible()
  await expect(legacyRow).toContainText('Local')

  const projectedRow = page.getByRole('row', { name: new RegExp(PROJECTED_SUBJECT) })
  await expect(projectedRow).toBeVisible()
  await expect(projectedRow).toContainText('k3g_crm')
  await expect(projectedRow).toContainText('#28176')
  await expect(projectedRow.getByText('Local')).toHaveCount(0)
})

test('CSV export includes real external projection metadata alongside empty legacy columns', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/tickets')
  await expect(page.getByRole('row', { name: new RegExp(PROJECTED_SUBJECT) })).toBeVisible()

  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('button', { name: /Exportar CSV/ }).click()
  const download = await downloadPromise
  const path = await download.path()
  const fs = await import('node:fs')
  const content = fs.readFileSync(path!, 'utf8')

  expect(content).toContain(
    'id,conversation_id,subject,status,priority,assigned_to,created_at,updated_at,provider,external_ticket_id,external_status,external_status_label,sync_status,last_synced_at'
  )
  const lines = content.split('\n')
  const legacyLine = lines.find((l) => l.includes(SEEDED_SUBJECT))
  const projectedLine = lines.find((l) => l.includes(PROJECTED_SUBJECT))
  expect(legacyLine).toBeTruthy()
  expect(projectedLine).toBeTruthy()

  // Legacy row: the six projection columns are the last six fields, all empty.
  const legacyCols = legacyLine!.split(',')
  expect(legacyCols.slice(-6)).toEqual(['', '', '', '', '', ''])

  expect(projectedLine).toContain('k3g_crm,28176,1,Novo,synced,')
})
