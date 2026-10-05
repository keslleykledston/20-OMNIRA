import { describe, it, expect } from 'vitest'
import { reportsAPI } from '../lib/api'

// O login mock que rodava no browser saiu: emitia um JWT com assinatura falsa
// que o backend nunca aceitou. O acesso de desenvolvimento agora é uma rota do
// servidor, coberta em session.test.ts.
describe('API Mock', () => {
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
