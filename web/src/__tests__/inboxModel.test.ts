import { describe, expect, it } from 'vitest';
import { dayLabel, inboxTimeLabel, previewText, sortChronological, waitInfo } from '../lib/inboxModel';
import type { MessageItem } from '../types/api';

const NOW = new Date(2026, 9, 3, 15, 0, 0); // 3 Oct 2026 15:00 local
const at = (d: number, h: number, m = 0) => new Date(2026, 9, d, h, m, 0).toISOString();

describe('inboxTimeLabel (WhatsApp-style)', () => {
  it('shows the time today, "Ontem", the weekday within a week, then the date', () => {
    expect(inboxTimeLabel(at(3, 9, 5), NOW)).toBe('09:05');
    expect(inboxTimeLabel(at(2, 23, 59), NOW)).toBe('Ontem');
    expect(inboxTimeLabel(at(1, 10), NOW)).toMatch(/^qui/i); // 1 Oct 2026 was a Thursday, 2 days back
    expect(inboxTimeLabel(new Date(2026, 8, 20, 10).toISOString(), NOW)).toBe('20/09/26');
  });
  it('is empty for missing or invalid dates', () => {
    expect(inboxTimeLabel(undefined, NOW)).toBe('');
    expect(inboxTimeLabel('not-a-date', NOW)).toBe('');
  });
});

describe('waitInfo — how long the customer has been waiting', () => {
  it('is null when nobody is waiting', () => {
    expect(waitInfo(undefined, NOW)).toBeNull();
    expect(waitInfo('garbage', NOW)).toBeNull();
  });
  it('uses compact units and escalates the tone', () => {
    expect(waitInfo(at(3, 14, 45), NOW)).toEqual({ label: '15 min', tone: 'muted' });
    expect(waitInfo(at(3, 14, 15), NOW)).toEqual({ label: '45 min', tone: 'warning' });
    expect(waitInfo(at(3, 11, 0), NOW)).toEqual({ label: '4 h', tone: 'danger' });
    expect(waitInfo(at(1, 15, 0), NOW)).toEqual({ label: '2 d', tone: 'danger' });
  });
  it('never shows 0 min for a message that just arrived', () => {
    expect(waitInfo(NOW.toISOString(), NOW)).toEqual({ label: '1 min', tone: 'muted' });
  });
});

describe('previewText', () => {
  const base = { last_message_at: at(3, 10) };
  it('shows the text, prefixing "Você: " for our own messages', () => {
    expect(previewText({ ...base, last_message_preview: 'oi  tudo\nbem?', last_message_direction: 'inbound' })).toBe('oi tudo bem?');
    expect(previewText({ ...base, last_message_preview: 'ok', last_message_direction: 'outbound' })).toBe('Você: ok');
  });
  it('names media by kind when there is no text, and never invents content', () => {
    expect(previewText({ ...base, last_message_preview: '', last_message_type: 'image', last_message_direction: 'inbound' })).toBe('Foto');
    expect(previewText({ ...base, last_message_preview: '', last_message_type: 'ptt', last_message_direction: 'inbound' })).toBe('Áudio');
    expect(previewText({ ...base, last_message_preview: '', last_message_type: 'whatever', last_message_direction: 'inbound' })).toBe('Mensagem');
    expect(previewText({})).toBe('Sem mensagens');
  });
});

describe('thread helpers', () => {
  const msg = (id: string, created_at: string): MessageItem => ({ id, conversation_id: 'c', body: id, direction: 'inbound', status: 'received', created_at });

  it('sortChronological puts the newest LAST and drops duplicates from overlapping pages', () => {
    const out = sortChronological([msg('c', at(3, 12)), msg('b', at(3, 11)), msg('b', at(3, 11)), msg('a', at(3, 10))]);
    expect(out.map((m) => m.id)).toEqual(['a', 'b', 'c']);
  });
  it('breaks timestamp ties by id so the order is stable', () => {
    expect(sortChronological([msg('2', at(3, 10)), msg('1', at(3, 10))]).map((m) => m.id)).toEqual(['1', '2']);
  });
  it('dayLabel says Hoje / Ontem, else the full date', () => {
    expect(dayLabel(at(3, 1), NOW)).toBe('Hoje');
    expect(dayLabel(at(2, 20), NOW)).toBe('Ontem');
    expect(dayLabel(at(1, 12), NOW)).toContain('outubro');
  });
});
