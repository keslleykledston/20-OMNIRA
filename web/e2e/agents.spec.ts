import { test, expect, type Page } from '@playwright/test'

// Real stack: who attends and in which queues is managed in the person's sheet in Pessoas (POST/PATCH/DELETE /agents/{id}/queues).
const ADMIN = 'admin@omnira.local'
const SUPERVISOR = 'supervisor@omnira.local'
const AGENT = 'test@omnira.local'

async function login(page: Page, email: string) {
  await page.goto('/login')
  await page.getByLabel('E-mail').fill(email)
  await page.getByRole('button', { name: /Entrar/ }).click()
  await page.waitForURL('/inbox', { timeout: 10_000 })
}

test('admin manages queue participation, availability and capacity from the person sheet', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/settings/people')
  await expect(page.getByRole('heading', { name: 'Pessoas' })).toBeVisible()
  await page.getByRole('button', { name: `Abrir a ficha de ${AGENT}` }).click()
  const sheet = page.getByRole('dialog', { name: `Ficha de ${AGENT}` })
  // the checkboxes follow the server (they change after the API answers): click and wait instead of Playwright's check()
  const seat = sheet.getByLabel('Participa da fila Default')
  if (!(await seat.isChecked())) {
    await seat.click()
    await expect(seat).toBeChecked()
  }
  const available = sheet.getByRole('switch', { name: 'Disponível na fila Default' })
  const before = await available.getAttribute('aria-checked')
  await available.click()
  await expect(available).not.toHaveAttribute('aria-checked', before ?? '')
  await sheet.getByRole('button', { name: 'Aumentar: Capacidade na fila Default' }).click()
  await seat.click()
  await expect(seat).not.toBeChecked()
  await seat.click()
  await expect(seat).toBeChecked()
  const attend = sheet.getByRole('switch', { name: 'Recebe conversas' })
  await attend.click()
  await expect(sheet.getByText('Em pausa. As filas ficaram guardadas.')).toBeVisible()
  await sheet.getByRole('button', { name: 'Voltar a atender' }).click()
  await expect(attend).toHaveAttribute('aria-checked', 'true')
})

test('supervisor manages who attends but not functions or access; an agent has no access to Pessoas', async ({ page }) => {
  await login(page, SUPERVISOR)
  await page.goto('/settings/people')
  await page.getByRole('button', { name: `Abrir a ficha de ${AGENT}` }).click()
  const sheet = page.getByRole('dialog', { name: `Ficha de ${AGENT}` })
  await expect(sheet.getByRole('switch', { name: 'Recebe conversas' })).toBeEnabled()
  await expect(sheet.getByRole('radio')).toHaveCount(0)
  await expect(sheet.getByRole('button', { name: 'Suspender acesso' })).toHaveCount(0)

  await login(page, AGENT)
  await page.goto('/settings/people')
  await expect(page.getByText('Você não tem permissão para ver as pessoas desta instância.')).toBeVisible()
})
