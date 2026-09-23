import { test, expect, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'

// PRODUCT.2-B: /tickets is now a REAL canonical listing (GET .../tickets,
// ticket.read-gated, same tickets table TicketPanel already uses for its
// per-conversation ticket — see ticket-panel.spec.ts). No mock ticket data.
const DB = process.env.E2E_DB || 'omnira_e2e'
const PG = process.env.E2E_PG_CONTAINER || 'omnira-postgres'
const CONV = 'd0d0d0d0-0000-0000-0000-000000000001'
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
test.beforeAll(() => {
  sql(`INSERT INTO tickets (id, tenant_id, conversation_id, status, priority, subject, created_at, updated_at)
       SELECT '99999999-0000-0000-0000-000000000001', tenant_id, id, 'open', 'high', '${SEEDED_SUBJECT}', now(), now()
       FROM conversations WHERE id='${CONV}'
       ON CONFLICT (id) DO UPDATE SET status='open', updated_at=now()`)
})
test.afterAll(() => {
  sql(`DELETE FROM tickets WHERE id='99999999-0000-0000-0000-000000000001'`)
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
