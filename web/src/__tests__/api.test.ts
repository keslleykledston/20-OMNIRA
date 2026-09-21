import { describe, it, expect } from 'vitest'
import { accountsAPI, ticketsAPI, reportsAPI } from '../lib/api'

// O login mock que rodava no browser saiu: emitia um JWT com assinatura falsa
// que o backend nunca aceitou. O acesso de desenvolvimento agora é uma rota do
// servidor, coberta em session.test.ts.
describe('API Mock', () => {
  describe('accountsAPI', () => {
    it('deve listar contas', async () => {
      const res = await accountsAPI.list()
      expect(Array.isArray(res.data)).toBe(true)
      expect(res.data.length).toBeGreaterThan(0)
    })

    it('deve ter propriedades obrigatórias em cada conta', async () => {
      const res = await accountsAPI.list()
      res.data.forEach((acc: any) => {
        expect(acc).toHaveProperty('id')
        expect(acc).toHaveProperty('name')
        expect(acc).toHaveProperty('account_type')
        expect(acc).toHaveProperty('status')
      })
    })

    it('deve criar conta com sucesso', async () => {
      const res = await accountsAPI.create({
        name: 'Test Account',
        account_type: 'operator'
      })
      expect(res.data).toHaveProperty('id')
      expect(res.data.name).toBe('Test Account')
      expect(res.data).toHaveProperty('created_at')
    })
  })

  describe('ticketsAPI', () => {
    it('deve listar tickets', async () => {
      const res = await ticketsAPI.list()
      expect(Array.isArray(res.data)).toBe(true)
    })

    it('deve filtrar por status', async () => {
      const res = await ticketsAPI.list({ status: 'open' })
      res.data.forEach((t: any) => {
        expect(t.status).toBe('open')
      })
    })

    it('deve criar ticket', async () => {
      const res = await ticketsAPI.create({
        title: 'Test Ticket',
        priority: 'high'
      })
      expect(res.data).toHaveProperty('id')
      expect(res.data.status).toBe('open')
    })

    it('deve resolver ticket', async () => {
      const res = await ticketsAPI.resolve('TKT-001')
      expect(res.data.status).toBe('resolved')
    })
  })

  describe('reportsAPI', () => {
    it('deve listar templates', async () => {
      const res = await reportsAPI.list()
      expect(Array.isArray(res.data)).toBe(true)
      expect(res.data.length).toBeGreaterThan(0)
    })

    it('deve gerar relatório', async () => {
      const res = await reportsAPI.generate('sla-compliance')
      expect(res.data).toHaveProperty('id')
      expect(res.data).toHaveProperty('data')
      expect(res.data.status).toBe('completed')
    })
  })
})
