import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import { API_BASE } from '../../lib/config'
import { authHeaders, currentUserId, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session'
import { getActingName } from '../../lib/acting'
import { useAccess } from '../../lib/useAccess'
import { ContactDetailsEditor } from '../contacts/ContactDetailsEditor'
import { ContactKindControl } from '../contacts/ContactKindControl'
import { ContactNotes } from '../contacts/ContactNotes'
import AttendanceMemoryPanel from './AttendanceMemoryPanel'
import HistorySearch from './HistorySearch'
import { Button } from '../primitives'
import DelegatedTransferDialog from './DelegatedTransferDialog'
import ConversationSummary from './ConversationSummary'
import { TicketPanel } from '../TicketPanel'
import { CollapsibleSection } from './CollapsibleSection'
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

// The details of an attendance for a person attending an instance through the Hub (ADR-0040): who the customer is and where the conversation stands, and,
// with the key contact.classify, the same controls a member has to say who the contact is (kind, companies, name and e-mail; phase 04a) and, with
// ticket.create / ticket.read, the instance's ERP ticket of the conversation (phase 04b: open it and see it; refreshing and changing the ERP status are not
// part of the delegated context yet), with conversation.transfer / conversation.claim handing over or giving back a conversation the person holds, and with
// contact.note the notes of the contact (phase 04c), and with ai.assist the AI summary of the conversation (phase 05; the text goes to the platform's AI provider, so it is only offered with that key)., with attendance.history the memory of the contact (earlier attendances and open items) and with followup.resolve marking an item done or dropped (phase 06), and (phase 09) with attendance.history AND conversation.read the search over the earlier messages of the contact. The keys come from the server (/me/access answers with the DELEGATED keys in this
// context), so the controls are only offered when the server will accept them; the server still decides every request. It reads the same conversation
// query the chat uses (one request) and the contact card (the key contact.read; without it the card is simply not shown).
export default function DelegatedContextPane({ conversationId }: { conversationId: string; onOpenConversation?: (id: string) => void }) {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const { can } = useAccess()
  const [transferOpen, setTransferOpen] = useState(false)
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
  const refreshAll = () => {
    void queryClient.invalidateQueries({ queryKey: ['inbox-conversation-detail', tenantId, conversationId] })
    void queryClient.invalidateQueries({ queryKey: ['delegated-contact', tenantId, contactId] })
    void queryClient.invalidateQueries({ queryKey: ['contact', tenantId, contactId] })
    void queryClient.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] })
  }
  const me = currentUserId()
  const heldByMe = !!me && c?.assigned_to_user_id === me && c?.status !== 'closed'
  const owner = !c?.assigned_to_user_id ? 'Sem responsável' : c.assigned_to_name?.trim() || 'Outro atendente'
  return (
    <aside aria-label="Detalhes do atendimento" className="flex h-full min-h-0 flex-col overflow-y-auto bg-surface">
      <header className="border-b border-border-subtle px-4 py-3">
        <h2 className="text-sm font-semibold text-text-primary">Detalhes do atendimento</h2>
        <p className="text-[12px] text-text-secondary">Atendendo {getActingName() || 'a instância'} pelo Hub</p>
      </header>
      {/* Same collapsible sections as the member's pane (CollapsibleSection remembers each choice per viewer). All open by default: nothing a
          delegated agent could already reach is hidden behind a click. */}
      <CollapsibleSection id="d-contact" title="Contato" icon="contacts" defaultOpen>
        <section aria-label="Contato" className="px-4 pb-4">
          <h3 className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-text-tertiary">Contato</h3>
          <p className="text-[15px] font-semibold text-text-primary">{name}</p>
          {whatsapp && <p className="text-[12px] text-text-secondary">{whatsapp} no WhatsApp</p>}
          {(card?.phone_e164 || c?.contact_phone) && <p className="text-[13px] text-text-secondary">{card?.phone_e164 || c?.contact_phone}</p>}
          {card?.email && <p className="text-[13px] text-text-secondary">{card.email}</p>}
          {kind && <p className="mt-1 text-[12px] text-text-secondary">{KIND[kind] ?? kind}</p>}
          {contact.isError && <p className="mt-1 text-[12px] text-text-tertiary">Os dados completos do contato não estão liberados para o seu acesso.</p>}
        </section>
      </CollapsibleSection>
      <CollapsibleSection id="d-attendance" title="Atendimento" icon="conversations" defaultOpen>
        <section aria-label="Atendimento" className="px-4 pb-4">
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
          {heldByMe && (can('conversation.transfer') || can('conversation.claim')) && (
            <div className="mt-2 flex justify-end">
              <Button variant="secondary" size="sm" onClick={() => setTransferOpen(true)}>Transferir ou devolver</Button>
            </div>
          )}
        </section>
      </CollapsibleSection>
      {contactId && can('contact.classify') && (
        <CollapsibleSection id="d-classify" title="Classificar o contato" icon="contacts" defaultOpen>
          <section aria-label="Classificar o contato">
            <ContactDetailsEditor contactId={contactId} onChanged={refreshAll} />
            <ContactKindControl
              contactId={contactId}
              kind={c?.contact_kind || 'unclassified'}
              internalRole={c?.contact_internal_role}
              contactName={c?.contact_name}
              delegated
              onChanged={refreshAll}
            />
          </section>
        </CollapsibleSection>
      )}
      {(can('ticket.create') || can('ticket.read')) && (
        <CollapsibleSection id="d-ticket" title="Chamado" icon="tickets" defaultOpen>
          <section aria-label="Chamado no ERP">
            <TicketPanel conversationId={conversationId} delegated conversationUnassigned={!c?.assigned_to_user_id} />
          </section>
        </CollapsibleSection>
      )}
      {can('ai.assist') && (
        <CollapsibleSection id="d-summary" title="Resumo com IA" icon="sparkles" defaultOpen>
          <section aria-label="Resumo por IA">
            <ConversationSummary conversationId={conversationId} />
          </section>
        </CollapsibleSection>
      )}
      {contactId && can('contact.note') && (
        <CollapsibleSection id="d-notes" title="Anotações internas" icon="tickets" defaultOpen>
          <section aria-label="Anotações">
            <ContactNotes contactId={contactId} />
          </section>
        </CollapsibleSection>
      )}
      {can('attendance.history') ? (
        <CollapsibleSection id="d-history" title="Histórico do contato" icon="conversations" defaultOpen>
          <div>
            {can('conversation.read') && <HistorySearch conversationId={conversationId} />}
            <AttendanceMemoryPanel conversationId={conversationId} delegated canResolve={can('followup.resolve')} />
          </div>
        </CollapsibleSection>
      ) : (
        <p role="note" className="m-4 rounded-control bg-surface-muted px-3 py-2 text-[12px] text-text-secondary">
          O histórico dos atendimentos anteriores não está liberado para você nesta empresa.
        </p>
      )}
      <DelegatedTransferDialog
        conversationId={conversationId}
        open={transferOpen}
        canTransfer={can('conversation.transfer')}
        onClose={() => setTransferOpen(false)}
        onDone={() => {
          setTransferOpen(false)
          refreshAll()
        }}
      />
    </aside>
  )
}
