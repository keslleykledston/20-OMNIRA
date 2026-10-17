import { useQuery } from '@tanstack/react-query'
import { serviceAPI } from '../../lib/serviceContext'
import type { DelegatedTarget } from '../../lib/attendance'
import { getTenantId } from '../../lib/session'
import { getActingHub } from '../../lib/acting'

// The cache key of "the protocol of this conversation", shared by the panel and the tag so one request serves both.
// The cache is per viewer's capacity AND per Hub: two Hubs serving the same company must never share what was loaded for the other.
export function serviceSessionKey(conversationId: string, hub?: DelegatedTarget, delegated = false) {
  const tenantId = hub?.tenantId ?? getTenantId()
  const kind = hub || delegated ? `delegated:${hub?.hubId ?? getActingHub()}` : 'member'
  return ['service-session', kind, tenantId, conversationId] as const
}

// The protocol (KTG-1001) beside the conversation, where the person is reading it. Nothing is shown when there is no
// session yet, the module is off or the viewer may not read it; the panel in the side pane remains the place to act.
export default function ProtocolTag({ conversationId, hub, delegated = false }: { conversationId: string; hub?: DelegatedTarget; delegated?: boolean }) {
  const tenantId = hub?.tenantId ?? getTenantId()
  const q = useQuery({
    queryKey: serviceSessionKey(conversationId, hub, delegated),
    queryFn: () => serviceAPI.ofConversation(conversationId, hub),
    enabled: !!tenantId && !!conversationId,
    retry: false,
  })
  if (!q.data) return null
  const closed = q.data.status === 'closed' || q.data.status === 'abandoned'
  return (
    <span data-testid="chat-protocol" className={`font-medium ${closed ? 'text-text-tertiary' : 'text-text-secondary'}`} title={closed ? 'Atendimento com protocolo encerrado' : 'Protocolo do atendimento'}>
      · Protocolo {q.data.protocol}
    </span>
  )
}
