import axios from 'axios'
import { useQuery } from '@tanstack/react-query'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

/** One WhatsApp line of the tenant a conversation can run on (the Inbox channel selector). */
export interface ChannelLine {
  id: string
  provider: string
  provider_kind: 'official' | 'unofficial'
  label: string
  number?: string
  status: string
  can_send_text: boolean
  window_required: boolean
}

/** The line a conversation is on, and whether its provider accepts free text right now. */
export interface ConversationChannel {
  channel_connection_id?: string
  provider?: string
  can_send_text: boolean
  /** The server has outbound files on and this line's provider can deliver them (ADR-0024). */
  can_send_media?: boolean
  window_required: boolean
  window_open: boolean
  last_inbound_at?: string
  window_expires_at?: string
}

async function guarded<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

export function useChannelLines() {
  const tenantId = getTenantId()
  return useQuery({
    queryKey: ['channel-lines', tenantId],
    queryFn: async () => {
      const d = await guarded(() => axios.get(`${API_BASE}/tenants/${tenantId}/channels/lines`, { headers: authHeaders() }))
      return ((d as { items?: ChannelLine[] })?.items ?? []) as ChannelLine[]
    },
    enabled: !!tenantId,
    staleTime: 60_000,
    retry: false,
  })
}

export function useConversationChannel(conversationId: string | null | undefined) {
  const tenantId = getTenantId()
  return useQuery({
    queryKey: ['conversation-channel', tenantId, conversationId],
    queryFn: () =>
      guarded(() =>
        axios.get<ConversationChannel>(`${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/channel`, { headers: authHeaders() }),
      ),
    enabled: !!tenantId && !!conversationId,
    retry: false,
  })
}

/** Opens (or finds) the contact's conversation on that line. Sends nothing. */
export async function openConversationOnChannel(contactId: string, channelConnectionId: string): Promise<{ conversation_id: string; created: boolean }> {
  const tenantId = getTenantId()
  return guarded(() =>
    axios.post(
      `${API_BASE}/tenants/${tenantId}/inbox/conversations/open`,
      { contact_id: contactId, channel_connection_id: channelConnectionId },
      { headers: authHeaders() },
    ),
  )
}

export function openErrorMessage(err: unknown): string {
  const status = (err as { response?: { status?: number } })?.response?.status
  if (status === 403) return 'Você não tem permissão para abrir conversas.'
  if (status === 422) return 'Este contato não pode ser contatado por esse canal (telefone inválido ou canal inativo).'
  return 'Não foi possível abrir a conversa neste canal.'
}

/** Short badge text: the kind of line plus the last digits, e.g. "Oficial ·7378". */
export function lineShortLabel(line: Pick<ChannelLine, 'provider_kind' | 'number'>): string {
  const base = line.provider_kind === 'official' ? 'Oficial' : 'WhatsApp'
  const digits = (line.number ?? '').replace(/\D/g, '')
  return digits.length >= 4 ? `${base} ·${digits.slice(-4)}` : base
}
