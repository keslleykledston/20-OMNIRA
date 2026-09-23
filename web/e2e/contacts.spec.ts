import { test, expect, type Page } from '@playwright/test'

// Real stack (scripts/e2e-inbox.sh). Contacts fixture: web/e2e/fixtures.sql
// seeds 'Maria Souza' for the pilot tenant.
const ADMIN = 'admin@omnira.local'

async function login(page: Page, email: string) {
  await page.goto('/login')
  await page.getByLabel('E-mail').fill(email)
  await page.getByRole('button', { name: /Entrar/ }).click()
  await page.waitForURL('/', { timeout: 10_000 })
}

test('list shows a real contact and opens its detail', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/contacts')
  await expect(page.getByRole('heading', { name: 'Contatos' })).toBeVisible()

  const row = page.getByRole('row', { name: /Maria Souza/ })
  await expect(row).toBeVisible()
  await row.click()

  await expect(page).toHaveURL(/\/contacts\/[0-9a-f-]+$/)
  await expect(page.getByRole('heading', { name: 'Maria Souza' })).toBeVisible()
  // Phone appears twice by design: identity header + copyable profile field.
  await expect(page.getByText('+55 11 98888-7777').first()).toBeVisible()
})

test('detail back link returns to the contacts list', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/contacts')
  await page.getByRole('row', { name: /Maria Souza/ }).click()
  await expect(page.getByRole('heading', { name: 'Maria Souza' })).toBeVisible()

  await page.getByRole('button', { name: 'Contatos' }).click()
  await expect(page).toHaveURL(/\/contacts$/)
  await expect(page.getByRole('heading', { name: 'Contatos' })).toBeVisible()
})
