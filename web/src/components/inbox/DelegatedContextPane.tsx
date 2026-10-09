import { useQuery } from '@tanstack/react-query'
import axios from 'axios'
import { API_BASE } from '../../lib/config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session'
import { getActingName } from '../../lib/acting'
import type { ConversationItem } from '../../types/api'

interface ContactCard {
  id: string
  display_name?: string
  alias?: string
  whatsapp_name?: string
  phone_e164?: string
  email?: string
  kind?: string
  open_conversation_count?: number
}

const KIND: Record<string, string> = { customer: 'Cliente', internal: 'Interno', other: 'Outro', spam: 'Spam ou golpe', unclassified: 'Ainda não classificado' }

// The details of an attendance for a person attending an instance through the Hub (ADR-0040 phase 03): who the customer is and where the conversation
// stands. Read-only on purpose: classifying or editing the contact, opening a ticket in the ERP, notes and memory arrive with phase 04, each with its own
// permission. It reads the same conversation query the chat uses (one request) and the contact card (the key contact.read; without it the card is simply not shown).
export default function DelegatedContextPane({ conversationId }: { conversationId: string; onOpenConversation?: (id: string) => void }) {
  const tenantId = getTenantId()
  const conversation = useQuery({
    queryKey: ['inbox-conversation-detail', tenantId, conversationId],
    queryFn: async () => {
      try {
        return (await axios.get(`${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}`, { headers: authHeaders() })).data as ConversationItem
      } catch (err) {
        if (isUnauthorized(err)) handleUnauthorized()
        throw err
      }
    },
    enabled: !!tenantId && !!conversationId,
  })
  const contactId = conversation.data?.contact_id
  const contact = useQuery({
    queryKey: ['delegated-contact', tenantId, contactId],
    queryFn: async () => {
      try {
        return (await axios.get(`${API_BASE}/tenants/${tenantId}/contacts/${contactId}`, { headers: authHeaders() })).data as ContactCard
      } catch (err) {
        if (isUnauthorized(err)) handleUnauthorized()
        throw err
      }
    },
    enabled: !!tenantId && !!contactId,
    retry: false,
  })
  const c = conversation.data
  const card = contact.data
  const name = card?.alias || card?.display_name || c?.contact_name || 'Sem nome'
  const whatsapp = card?.whatsapp_name && card.whatsapp_name !== name ? card.whatsapp_name : ''
  const kind = card?.kind || c?.contact_kind || ''
  const owner = !c?.assigned_to_user_id ? 'Sem responsável' : c.assigned_to_name?.trim() || 'Outro atendente'
  return (
    <aside aria-label="Detalhes do atendimento" className="flex h-full min-h-0 flex-col overflow-y-auto bg-surface">
      <header className="border-b border-border-subtle px-4 py-3">
        <h2 className="text-sm font-semibold text-text-primary">Detalhes do atendimento</h2>
        <p className="text-[12px] text-text-secondary">Atendendo {getActingName() || 'a instância'} pelo Hub</p>
      </header>
      <div className="space-y-4 px-4 py-4">
        <section aria-label="Contato">
          <h3 className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-text-tertiary">Contato</h3>
          <p className="text-[15px] font-semibold text-text-primary">{name}</p>
          {whatsapp && <p className="text-[12px] text-text-secondary">{whatsapp} no WhatsApp</p>}
          {(card?.phone_e164 || c?.contact_phone) && <p className="text-[13px] text-text-secondary">{card?.phone_e164 || c?.contact_phone}</p>}
          {card?.email && <p className="text-[13px] text-text-secondary">{card.email}</p>}
          {kind && <p className="mt-1 text-[12px] text-text-secondary">{KIND[kind] ?? kind}</p>}
          {contact.isError && <p className="mt-1 text-[12px] text-text-tertiary">Os dados completos do contato não estão liberados para o seu acesso.</p>}
        </section>
        <section aria-label="Atendimento">
          <h3 className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-text-tertiary">Atendimento</h3>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[13px]">
            <dt className="text-text-secondary">Estado</dt>
            <dd className="text-right text-text-primary">{c?.status === 'closed' ? 'Finalizado' : 'Em atendimento'}</dd>
            <dt className="text-text-secondary">Responsável</dt>
            <dd className="text-right text-text-primary">{owner}</dd>
            {typeof c?.message_count === 'number' && (
              <>
                <dt className="text-text-secondary">Mensagens</dt>
                <dd className="text-right text-text-primary">{c.message_count}</dd>
              </>
            )}
          </dl>
        </section>
        <p role="note" className="rounded-control bg-surface-muted px-3 py-2 text-[12px] text-text-secondary">
          Classificar ou editar o contato, abrir chamado no ERP, notas e histórico chegam na próxima etapa.
        </p>
      </div>
    </aside>
  )
}
