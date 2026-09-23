import { describe, it, expect } from 'vitest'
import { accountsAPI, reportsAPI } from '../lib/api'

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
