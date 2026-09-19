import { describe, it, expect } from 'vitest'
import { authAPI, accountsAPI, ticketsAPI, reportsAPI } from '../lib/api'

describe('API Mock', () => {
  describe('authAPI', () => {
    it('deve fazer login com email válido', async () => {
      const res = await authAPI.login('test@omnira.local', 'password')
      expect(res.data).toHaveProperty('token')
      expect(res.data).toHaveProperty('user')
      expect(res.data.user.email).toBe('test@omnira.local')
    })

    it('deve rejeitar email inválido', async () => {
      try {
        await authAPI.login('invalid@email.com', 'password')
        throw new Error('Should have rejected')
      } catch (error: any) {
        expect(error.response.status).toBe(401)
      }
    })

    it('token deve ter formato JWT válido', async () => {
      const res = await authAPI.login('test@omnira.local', 'pass')
      const token = res.data.token
      const parts = token.split('.')
      expect(parts).toHaveLength(3)
    })
  })

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
