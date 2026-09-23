import { test, expect, type Page } from '@playwright/test'

// PRODUCT.3-B: `/` is now a REAL V1 operational snapshot — three durable
// Postgres counts (GET .../dashboard/snapshot, dashboard.read-gated) plus
// agents online composed from the same real presence snapshot/SSE already
// proven by presence.spec.ts/supervisor.spec.ts. No mock chart/trend/SLA
// content.
const ADMIN = 'admin@omnira.local'
const AGENT = 'test@omnira.local'

async function login(page: Page, email: string) {
  await page.goto('/login')
  await page.getByLabel('E-mail').fill(email)
  await page.getByRole('button', { name: /Entrar/ }).click()
  await page.waitForURL('/inbox', { timeout: 10_000 })
}

test('admin sees the four real V1 cards with real Postgres-backed counts', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/')

  const main = page.locator('main')
  for (const label of ['Conversas abertas', 'Tickets abertos', 'Contatos', 'Agentes online']) {
    const card = main.getByText(label, { exact: true }).locator('xpath=..')
    await expect(card).toBeVisible()
    // Real numeric value, not the '—' placeholder shown only while unauthorized/loading.
    await expect(card.getByText(/^\d+$/)).toBeVisible()
  }

  // No content from the retired mock Dashboard (charts, trends, fake activity, SLA).
  await expect(page.getByText('Conversas por canal')).toHaveCount(0)
  await expect(page.getByText('Status dos tickets')).toHaveCount(0)
  await expect(page.getByText(/Conformidade SLA/)).toHaveCount(0)
})

test('agents online reflects a real presence transition via the same SSE mechanism', async ({ browser }) => {
  const adminContext = await browser.newContext()
  const adminPage = await adminContext.newPage()
  await login(adminPage, ADMIN)
  await adminPage.goto('/')
  const agentsCard = adminPage.locator('main').getByText('Agentes online', { exact: true }).locator('xpath=..')
  await expect(agentsCard).toBeVisible()

  const agentContext = await browser.newContext()
  const agentPage = await agentContext.newPage()
  await login(agentPage, AGENT)
  // Layout fires a heartbeat on any authenticated route — no navigation to
  // /settings/agents needed on the agent's side (same mechanism as
  // presence.spec.ts/supervisor.spec.ts).

  // No reload on the admin page: only passes if the SSE transition arrived.
  await expect(agentsCard.getByText(/^[1-9]\d*$/)).toBeVisible({ timeout: 10_000 })

  await adminContext.close()
  await agentContext.close()
})

test('a user without dashboard.read is denied, not shown a fabricated snapshot', async ({ page }) => {
  await login(page, AGENT)
  await page.goto('/')
  await expect(page.getByText('Você não tem permissão para visualizar o Dashboard.')).toBeVisible()
  await expect(page.getByText('Conversas abertas')).toHaveCount(0)
})
