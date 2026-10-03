import { describe, expect, it } from 'vitest';
import { conversationPreview, formatInteraction, messageCountLabel } from '../lib/contactFormat';

// Local-time constructors on purpose: the helpers bucket by the viewer's calendar day.
const NOW = new Date(2026, 9, 3, 18, 0, 0);

describe('formatInteraction', () => {
  it('says "Hoje" for the same calendar day', () => {
    expect(formatInteraction(new Date(2026, 9, 3, 10, 24).toISOString(), NOW)).toBe('Hoje, 10:24');
  });

  it('says "Ontem" for the previous calendar day, even a few minutes apart', () => {
    expect(formatInteraction(new Date(2026, 9, 2, 23, 59).toISOString(), NOW)).toBe('Ontem, 23:59');
  });

  it('falls back to a short absolute date', () => {
    expect(formatInteraction(new Date(2026, 8, 20, 11, 20).toISOString(), NOW)).toMatch(/^20 set\.? 2026, 11:20$/);
  });

  it('renders an em dash for missing or unparseable input, never a made-up date', () => {
    expect(formatInteraction(null, NOW)).toBe('—');
    expect(formatInteraction(undefined, NOW)).toBe('—');
    expect(formatInteraction('not-a-date', NOW)).toBe('—');
  });
});

describe('conversationPreview', () => {
  const msg = (over: object = {}) => ({
    direction: 'inbound' as const,
    message_type: 'text' as const,
    body_preview: 'Olá',
    created_at: '2026-10-03T10:00:00Z',
    ...over,
  });

  it('prefixes the contact first name for inbound and "Você" for outbound', () => {
    expect(conversationPreview(msg(), 'Ana')).toBe('Ana: Olá');
    expect(conversationPreview(msg({ direction: 'outbound' }), 'Ana')).toBe('Você: Olá');
  });

  it('names the media kind when there is no caption', () => {
    expect(conversationPreview(msg({ message_type: 'audio', body_preview: '' }), 'Ana')).toBe('Ana: Áudio');
    expect(conversationPreview(msg({ message_type: 'sticker', body_preview: '  ' }), 'Ana')).toBe('Ana: Figurinha');
  });

  it('says there are no messages when there is no last message', () => {
    expect(conversationPreview(null, 'Ana')).toBe('Sem mensagens');
  });
});

describe('messageCountLabel', () => {
  it('pluralises', () => {
    expect(messageCountLabel(0)).toBe('0 mensagens');
    expect(messageCountLabel(1)).toBe('1 mensagem');
    expect(messageCountLabel(12)).toBe('12 mensagens');
  });
});
