import type { ConversationItem, MessageItem } from '../types/api';

export type InboxSegment = 'all' | 'waiting' | 'mine' | 'spam';

const DAY_MS = 86_400_000;

function startOfDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

/** WhatsApp-style time of the last activity: today HH:mm, "Ontem", weekday within a week, else dd/mm/aa. */
export function inboxTimeLabel(iso: string | undefined, now: Date = new Date()): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  const days = Math.round((startOfDay(now) - startOfDay(date)) / DAY_MS);
  if (days <= 0) return date.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' });
  if (days === 1) return 'Ontem';
  if (days < 7) return date.toLocaleDateString('pt-BR', { weekday: 'short' });
  return date.toLocaleDateString('pt-BR', { day: '2-digit', month: '2-digit', year: '2-digit' });
}

export type WaitTone = 'muted' | 'warning' | 'danger';

export interface WaitInfo {
  label: string;
  tone: WaitTone;
}

export interface WaitThresholds {
  warnMinutes: number;
  dangerMinutes: number;
}

// Display thresholds for the list only — not a contractual SLA, just "how long has this customer
// been waiting". Each tenant can change them in Configurações; these are the standard values.
export const DEFAULT_WAIT_THRESHOLDS: WaitThresholds = { warnMinutes: 30, dangerMinutes: 120 };

/** How long the customer has been waiting for an answer; null when nobody is waiting. */
export function waitInfo(
  waitingSince: string | undefined,
  now: Date = new Date(),
  thresholds: WaitThresholds = DEFAULT_WAIT_THRESHOLDS,
): WaitInfo | null {
  if (!waitingSince) return null;
  const since = new Date(waitingSince).getTime();
  if (Number.isNaN(since)) return null;
  const minutes = Math.max(1, Math.floor((now.getTime() - since) / 60_000));
  const label =
    minutes < 60 ? `${minutes} min` : minutes < 1440 ? `${Math.floor(minutes / 60)} h` : `${Math.floor(minutes / 1440)} d`;
  const tone: WaitTone =
    minutes >= thresholds.dangerMinutes ? 'danger' : minutes >= thresholds.warnMinutes ? 'warning' : 'muted';
  return { label, tone };
}

const TYPE_LABEL: Record<string, string> = {
  image: 'Foto',
  video: 'Vídeo',
  audio: 'Áudio',
  ptt: 'Áudio',
  voice: 'Áudio',
  document: 'Documento',
  sticker: 'Figurinha',
  location: 'Localização',
};

/** One-line preview of the last message ("Você: " when we wrote it; media shown by its kind). */
export function previewText(conv: Pick<ConversationItem, 'last_message_preview' | 'last_message_type' | 'last_message_direction' | 'last_message_at'>): string {
  if (!conv.last_message_at) return 'Sem mensagens';
  const body = (conv.last_message_preview ?? '').replace(/\s+/g, ' ').trim();
  const text = body || TYPE_LABEL[(conv.last_message_type ?? '').toLowerCase()] || 'Mensagem';
  return conv.last_message_direction === 'outbound' ? `Você: ${text}` : text;
}

/** Day separator in a thread: "Hoje", "Ontem", or the full date. */
export function dayLabel(iso: string, now: Date = new Date()): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  const days = Math.round((startOfDay(now) - startOfDay(date)) / DAY_MS);
  if (days === 0) return 'Hoje';
  if (days === 1) return 'Ontem';
  return date.toLocaleDateString('pt-BR', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
}

/** Messages oldest → newest (what a chat shows), de-duplicated by id: refetching paged results can overlap. */
export function sortChronological(messages: MessageItem[]): MessageItem[] {
  const byId = new Map<string, MessageItem>();
  for (const m of messages) byId.set(m.id, m);
  return [...byId.values()].sort((a, b) => {
    const t = new Date(a.created_at).getTime() - new Date(b.created_at).getTime();
    return t !== 0 ? t : a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}
