import type { Group, GroupMessage, GroupMessageType } from './groups'

const TYPE_LABEL: Record<GroupMessageType, string> = {
  text: '',
  image: 'Foto',
  video: 'Vídeo',
  audio: 'Áudio',
  document: 'Documento',
  sticker: 'Figurinha',
  location: 'Localização',
  other: 'Mídia',
}

/** What to show for a message: its text, or the kind of media when there is no text (media is not downloaded). */
export function messageText(m: Pick<GroupMessage, 'body' | 'message_type'>): string {
  const body = m.body.trim()
  if (body) return body
  const label = TYPE_LABEL[m.message_type]
  return label ? `[${label}]` : ''
}

export function authorLabel(m: Pick<GroupMessage, 'author_name' | 'from_me'>): string {
  if (m.from_me) return 'Você'
  return m.author_name.trim() || 'Participante'
}

/** One line for the group list: "Autor: texto". */
export function groupPreview(g: Pick<Group, 'last_message'>): string {
  const lm = g.last_message
  if (!lm) return 'Sem mensagens'
  const text = messageText({ body: lm.preview, message_type: lm.message_type }).replace(/\s+/g, ' ')
  return `${authorLabel(lm)}: ${text}`
}

/** Oldest first, de-duplicated by id (refetching paged results can overlap), ties broken by id. */
export function sortGroupMessages(messages: GroupMessage[]): GroupMessage[] {
  const byId = new Map<string, GroupMessage>()
  for (const m of messages) byId.set(m.id, m)
  return [...byId.values()].sort((a, b) => {
    const t = new Date(a.sent_at).getTime() - new Date(b.sent_at).getTime()
    return t !== 0 ? t : a.id < b.id ? -1 : a.id > b.id ? 1 : 0
  })
}
