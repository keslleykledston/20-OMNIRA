import { test, expect, type Page } from '@playwright/test'

const ADMIN = 'admin@omnira.local'
const SUPERVISOR = 'supervisor@omnira.local'
const AGENT = 'test@omnira.local'
const QUEUE_ID = 'dddddddd-dddd-dddd-dddd-dddddddddddd'

async function login(page: Page, email: string) {
  await page.goto('/login')
  await page.getByLabel('E-mail').fill(email)
  await page.getByRole('button', { name: /Entrar/ }).click()
  await page.waitForURL('/', { timeout: 10_000 })
}

test('admin manages operational queue assignment using real API', async ({ page }) => {
  await login(page, ADMIN)
  await page.goto('/settings/agents')
  await expect(page.getByRole('heading', { name: 'Agentes' })).toBeVisible()
  await page.getByRole('row', { name: /test@omnira\.local/ }).getByRole('button', { name: 'Detalhes' }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.getByLabel('Elegibilidade Default').uncheck()
  await expect(page.getByRole('status')).toContainText('Associação atualizada')
  await page.getByLabel('Capacidade Default').fill('2')
  await page.getByLabel('Capacidade Default').blur()
  await expect(page.getByRole('status')).toContainText('Associação atualizada')
  await page.getByRole('button', { name: 'Remover' }).click()
  await expect(page.getByText('Nenhuma fila atribuída.')).toBeVisible()
  await page.getByLabel('ID da fila').fill(QUEUE_ID)
  await page.getByLabel('Capacidade').fill('3')
  await page.getByRole('button', { name: 'Adicionar fila' }).click()
  await expect(page.getByText('Default', { exact: true })).toBeVisible()
  await page.getByRole('dialog').getByRole('button', { name: 'Fechar' }).last().click()
  const agentRow = page.getByRole('row', { name: /test@omnira\.local/ })
  await agentRow.getByRole('button', { name: 'Desativar' }).click()
  await expect(agentRow.getByRole('button', { name: 'Ativar' })).toBeVisible()
  await agentRow.getByRole('button', { name: 'Ativar' }).click()
  await expect(agentRow.getByRole('button', { name: 'Desativar' })).toBeVisible()
})

test('supervisor has operational management; agent does not', async ({ page }) => {
  await login(page, SUPERVISOR)
  await page.goto('/settings/agents')
  await expect(page.getByRole('button', { name: 'Detalhes' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: /Desativar|Ativar/ }).first()).toBeVisible()

  await login(page, AGENT)
  await page.goto('/settings/agents')
  await expect(page.getByText('Você não tem permissão para visualizar agentes.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Detalhes' })).toHaveCount(0)
})
