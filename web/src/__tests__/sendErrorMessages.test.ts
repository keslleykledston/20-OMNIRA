import { describe, expect, it } from 'vitest'
import { describeHubWriteError } from '../lib/hub'

const conflict = (body: string) => ({ response: { status: 409, data: body } })

describe('describeHubWriteError — 409 sem canal', () => {
  it('diz que falta canal ativo, não que a conversa mudou', () => {
    const msg = describeHubWriteError(conflict('conversation has no active text channel'))
    expect(msg).toContain('canal ativo')
    expect(msg).not.toContain('mudou')
  })

  it('mantém a mensagem genérica para um 409 desconhecido', () => {
    expect(describeHubWriteError(conflict('something else'))).toBe('A conversa mudou, tente novamente.')
  })
})
