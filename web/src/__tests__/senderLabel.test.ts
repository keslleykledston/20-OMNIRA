import { describe, it, expect } from 'vitest'
import { senderLabel, sameSpeaker } from '../lib/senderLabel'
import type { MessageItem } from '../types/api'

const msg = (over: Partial<MessageItem>): MessageItem => ({
  id: 'm', conversation_id: 'c', body: 'x', direction: 'outbound', status: 'sent', created_at: '2026-10-17T10:00:00Z', ...over,
})

describe('senderLabel', () => {
  it('names each kind of outbound sender, and the contact for inbound', () => {
    expect(senderLabel(msg({ sender_kind: 'agent' }))).toBe('Equipe')
    expect(senderLabel(msg({ sender_kind: 'automation' }))).toBe('Automação')
    expect(senderLabel(msg({ sender_kind: 'external_app' }))).toBe('Aplicativo externo')
    expect(senderLabel(msg({ sender_kind: 'device' }))).toBe('Celular da empresa')
    expect(senderLabel(msg({ direction: 'inbound' }), 'Maria')).toBe('Maria')
  })
  it('keeps the neutral name when an older server sends no kind', () => {
    expect(senderLabel(msg({}))).toBe('Equipe')
  })
  it('starts a new run when the kind changes on the same side', () => {
    expect(sameSpeaker(msg({ sender_kind: 'agent' }), msg({ sender_kind: 'agent' }))).toBe(true)
    expect(sameSpeaker(msg({ sender_kind: 'agent' }), msg({ sender_kind: 'automation' }))).toBe(false)
    expect(sameSpeaker(msg({ direction: 'inbound' }), msg({ direction: 'outbound' }))).toBe(false)
  })
})
