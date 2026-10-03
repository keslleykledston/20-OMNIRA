import { beforeEach, describe, expect, it, vi } from 'vitest'
import axios from 'axios'
import { queueErrorMessage, queuesAPI } from '../lib/queues'

vi.mock('axios')

describe('queuesAPI', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.setItem('tenantId', 'tenant-a-uuid')
    localStorage.setItem('token', 'tok')
  })

  it('lists, creates, updates and deletes through the tenant-scoped queues route', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: { items: [{ id: 'q1' }] } })
    vi.mocked(axios.post).mockResolvedValue({ data: { id: 'q2' } })
    vi.mocked(axios.patch).mockResolvedValue({ data: { id: 'q1' } })
    vi.mocked(axios.delete).mockResolvedValue({ data: undefined })

    await expect(queuesAPI.list()).resolves.toEqual([{ id: 'q1' }])
    await queuesAPI.create({ name: 'Suporte', mode: 'manual', is_default: false })
    await queuesAPI.update('q1', { name: 'Novo' })
    await queuesAPI.remove('q1')

    expect(String(vi.mocked(axios.get).mock.calls[0][0])).toMatch(/\/tenants\/tenant-a-uuid\/queues$/)
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ name: 'Suporte', mode: 'manual', is_default: false })
    expect(String(vi.mocked(axios.patch).mock.calls[0][0])).toMatch(/\/queues\/q1$/)
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ name: 'Novo' })
    expect(String(vi.mocked(axios.delete).mock.calls[0][0])).toMatch(/\/queues\/q1$/)
  })
})

describe('queueErrorMessage', () => {
  const err = (status: number, data = '') => ({ response: { status, data } })

  it('turns the API reasons into sentences the operator can act on', () => {
    expect(queueErrorMessage(err(409, 'a queue with this name already exists'))).toBe('Já existe um grupo com esse nome.')
    expect(queueErrorMessage(err(409, 'choose another queue as the default instead of unsetting it'))).toMatch(/escolha outro grupo/i)
    expect(queueErrorMessage(err(409, 'the default queue cannot be deleted; choose another default first'))).toMatch(/padrão não pode ser excluído/)
    expect(queueErrorMessage(err(409, 'the queue still has conversations and cannot be deleted'))).toMatch(/ainda tem conversas/)
    expect(queueErrorMessage(err(403, 'forbidden'))).toMatch(/permissão/)
    expect(queueErrorMessage(err(404, 'queue not found'))).toMatch(/não existe mais/)
    expect(queueErrorMessage(err(400, 'name must have 1 to 60 characters'))).toMatch(/1 a 60/)
  })

  it('falls back instead of leaking raw text for anything unknown', () => {
    expect(queueErrorMessage(err(500, 'boom: sql error'))).toBe('Não foi possível concluir a alteração.')
    expect(queueErrorMessage(undefined)).toBe('Não foi possível concluir a alteração.')
  })
})
