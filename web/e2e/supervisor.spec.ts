import { test, expect, type Page } from '@playwright/test'

// PRODUCT.1: /supervisor is now a REAL presence overview — same real roster
// (GET /agents) + real Valkey presence snapshot/SSE (GET /agents/presence,
// /agents/presence/events) already proven by agents.spec.ts/presence.spec.ts.
// No mock account/SLA/KPI data.
const ADMIN = 'admin@omnira.local'
const AGENT = 'test@omnira.local'

async function login(page: Page, email: string) {
  await page.goto('/login')
  await page.getByLabel('E-mail').fill(email)
  await page.getByRole('button', { name: /Entrar/ }).click()
  await page.waitForURL('/inbox', { timeout: 10_000 })
}

test('supervisor shows real roster and reflects a real online presence transition via SSE', async ({ browser }) => {
  const adminContext = await browser.newContext()
  const adminPage = await adminContext.newPage()
  await login(adminPage, ADMIN)
  await adminPage.goto('/supervisor')
  await expect(adminPage.getByRole('heading', { name: 'Supervisor' })).toBeVisible()

  const agentRow = adminPage.getByRole('row', { name: /test@omnira\.local/ })
  await expect(agentRow).toBeVisible()

  // No mock KPI/account/SLA content from the retired mock implementation.
  await expect(adminPage.getByText(/Conformidade SLA/)).toHaveCount(0)
  await expect(adminPage.getByText(/Saúde das Contas/)).toHaveCount(0)

  // Same real mechanism as presence.spec.ts: Layout fires a heartbeat on any
  // authenticated route, no navigation to /settings/agents needed.
  const agentContext = await browser.newContext()
  const agentPage = await agentContext.newPage()
  await login(agentPage, AGENT)

  // No reload on the supervisor page: only passes if the SSE transition arrived.
  await expect(agentRow.getByText('Online', { exact: true })).toBeVisible({ timeout: 10_000 })

  await adminContext.close()
  await agentContext.close()
})

test('a user without agent.read sees a permission message, not the roster', async ({ page }) => {
  await login(page, AGENT)
  await page.goto('/supervisor')
  await expect(page.getByText('Você não tem permissão para visualizar o supervisor.')).toBeVisible()
  await expect(page.getByRole('table')).toHaveCount(0)
})
