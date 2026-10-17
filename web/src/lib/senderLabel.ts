import type { MessageItem } from '../types/api'

/** Who spoke, as the timeline names it. `sender_kind` comes from the server and says only the kind, never an identity. */
export const SENDER_LABEL: Record<NonNullable<MessageItem['sender_kind']>, string> = {
  agent: 'Equipe',
  external_app: 'Aplicativo externo',
  automation: 'Automação',
  device: 'Celular da empresa',
}

export function senderLabel(msg: MessageItem, contactName?: string): string | undefined {
  if (msg.direction === 'inbound') return contactName
  // an older server sends no kind: keep the neutral name rather than guess
  return SENDER_LABEL[msg.sender_kind ?? 'agent'] ?? 'Equipe'
}

/** A new run starts when the side OR the kind of sender changes, so a bot line between two people is never merged into theirs. */
export function sameSpeaker(a: MessageItem, b: MessageItem): boolean {
  return a.direction === b.direction && (a.sender_kind ?? '') === (b.sender_kind ?? '')
}
