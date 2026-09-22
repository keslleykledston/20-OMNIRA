import { test, expect, type Page } from '@playwright/test'

// IAM4.2-A (ADR-0010): proves the browser/API/UI integration only — presence
// online/offline, per-session/tab, heartbeat, snapshot + live SSE update.
// TTL/expiry timing (120s) is deliberately NOT exercised here: it is covered
// deterministically by the Go/Valkey backend tests
// (internal/presence/adapters/valkey_integration_test.go), and slowing this
// browser suite down to wait it out would only make it flaky.

const ADMIN = 'admin@omnira.local'
const AGENT = 'test@omnira.local'

async function login(page: Page, email: string) {
  await page.goto('/login')
  await page.getByLabel('E-mail').fill(email)
  await page.getByRole('button', { name: /Entrar/ }).click()
  await page.waitForURL('/', { timeout: 10_000 })
}

test('agent heartbeat turns presence online and the already-open supervisor view updates live via SSE', async ({ browser }) => {
  const adminContext = await browser.newContext()
  const adminPage = await adminContext.newPage()
  await login(adminPage, ADMIN)
  await adminPage.goto('/settings/agents')
  await expect(adminPage.getByRole('heading', { name: 'Agentes' })).toBeVisible()
  const agentRow = adminPage.getByRole('row', { name: /test@omnira\.local/ })
  await expect(agentRow).toBeVisible()
  // The admin's presence column is now subscribed via SSE and holds the initial snapshot.

  const agentContext = await browser.newContext()
  const agentPage = await agentContext.newPage()
  await login(agentPage, AGENT)
  // Layout mounts on any authenticated route and fires the first heartbeat immediately —
  // no navigation to /settings/agents needed on the agent's side.

  // No reload on the admin page: this only passes if the SSE transition event actually arrived.
  await expect(agentRow.getByText('Online', { exact: true })).toBeVisible({ timeout: 10_000 })

  // A second tab/session for the same agent must not disturb presence (still just one online agent).
  const agentPage2 = await agentContext.newPage()
  await agentPage2.goto('/')
  await expect(agentRow.getByText('Online', { exact: true })).toBeVisible()

  // Closing one of two live sessions must not flip the agent offline (the other tab keeps it alive).
  // The 120s TTL/reaper path itself is out of scope here — see the backend integration tests.
  await agentPage.close()
  await expect(agentRow.getByText('Online', { exact: true })).toBeVisible()

  await adminContext.close()
  await agentContext.close()
})

test('repeated heartbeats do not create a duplicate transition or destabilize the indicator', async ({ browser }) => {
  const adminContext = await browser.newContext()
  const adminPage = await adminContext.newPage()
  await login(adminPage, ADMIN)
  await adminPage.goto('/settings/agents')
  const agentRow = adminPage.getByRole('row', { name: /test@omnira\.local/ })

  const agentContext = await browser.newContext()
  const agentPage = await agentContext.newPage()
  await login(agentPage, AGENT)
  await expect(agentRow.getByText('Online', { exact: true })).toBeVisible({ timeout: 10_000 })

  // Force extra heartbeats (focus/visibility) quickly: must stay a single, stable Online state.
  await agentPage.evaluate(() => window.dispatchEvent(new Event('focus')))
  await agentPage.waitForTimeout(300)
  await agentPage.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(agentRow.getByText('Online', { exact: true })).toBeVisible()
  await expect(agentRow.getByText('Online', { exact: true })).toHaveCount(1)

  await adminContext.close()
  await agentContext.close()
})
