import type { ContactLastMessage } from './contacts'

const MEDIA_LABELS: Record<ContactLastMessage['message_type'], string> = {
  text: '',
  image: 'Imagem',
  video: 'Vídeo',
  audio: 'Áudio',
  document: 'Documento',
  sticker: 'Figurinha',
}

function startOfDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
}

// "Hoje, 10:24" · "Ontem, 16:42" · "20 set. 2026, 11:20". An unparseable or
// missing instant renders as an em dash, never as a made-up date.
export function formatInteraction(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const time = d.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' })
  const days = Math.round((startOfDay(now) - startOfDay(d)) / 86_400_000)
  if (days === 0) return `Hoje, ${time}`
  if (days === 1) return `Ontem, ${time}`
  const parts = new Intl.DateTimeFormat('pt-BR', { day: 'numeric', month: 'short', year: 'numeric' }).formatToParts(d)
  const get = (type: string) => parts.find((p) => p.type === type)?.value ?? ''
  return `${get('day')} ${get('month')} ${get('year')}, ${time}`
}

// What the conversation row shows after "Você:" / "<Nome>:". An empty caption on
// a media message falls back to the media kind; no message at all says so.
export function conversationPreview(last: ContactLastMessage | null, firstName: string): string {
  if (!last) return 'Sem mensagens'
  const prefix = last.direction === 'outbound' ? 'Você: ' : `${firstName}: `
  return prefix + (last.body_preview.trim() || MEDIA_LABELS[last.message_type] || '')
}

export function messageCountLabel(n: number): string {
  return n === 1 ? '1 mensagem' : `${n} mensagens`
}
