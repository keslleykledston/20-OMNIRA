import { CollapsibleSection } from './CollapsibleSection'
import ServiceSessionPanel from '../service/ServiceSessionPanel'
import { TicketPanel } from '../TicketPanel'
import ConversationSummary from './ConversationSummary'
import RoutingPanel from './RoutingPanel'
import HistorySearch from './HistorySearch'
import AttendanceMemoryPanel from './AttendanceMemoryPanel'
import { ContactNotes } from '../contacts/ContactNotes'
import type { DelegatedTarget } from '../../lib/acting'

interface Props {
  /** The instance and the Hub the panels work on: every request names both and every cache is keyed by both. */
  target: DelegatedTarget
  conversationId: string
  contactId?: string
  /** The delegated keys the server says the person holds in this instance (it still decides every request). */
  can: (key: string) => boolean
  closed: boolean
  unassigned: boolean
  /** Companies the contact belongs to (main first): the ticket form offers only those when it can tell which they are. */
  companyNames?: string[]
  /** Prefix of the remembered open/closed state of each section: the two panes that use these keep their own choices. */
  idPrefix: string
}

// The panels a person attending an instance through the Hub gets, with the keys that decide each one (ADR-0040, ADR-0049): protocol and tasks, the ERP ticket, the AI summary,
// routing, the contact's notes and the memory of earlier attendances. They are the SAME components the member's workspace uses, asked to work on `target`. Shared by the
// Hub's unified inbox (HubContextPane) and the instance workspace of a Hub agent (DelegatedContextPane): one list of panels and keys, not two.
export default function DelegatedSections({ target, conversationId, contactId, can, closed, unassigned, companyNames, idPrefix }: Props) {
  return (
    <>
      {can('service.read') && (
        <CollapsibleSection id={`${idPrefix}-service-session`} title="Protocolo e tarefas" icon="tickets" defaultOpen>
          <ServiceSessionPanel conversationId={conversationId} can={can} hub={target} conversationClosed={closed} />
        </CollapsibleSection>
      )}
      {(can('ticket.create') || can('ticket.read')) && (
        <CollapsibleSection id={`${idPrefix}-ticket`} title="Chamado" icon="tickets" defaultOpen>
          <section aria-label="Chamado no ERP">
            <TicketPanel conversationId={conversationId} target={target} canRefresh={can('ticket.create')} canChangeStatus={can('ticket.update')} contactCompanyNames={companyNames} conversationUnassigned={unassigned} />
          </section>
        </CollapsibleSection>
      )}
      {can('ai.assist') && (
        <CollapsibleSection id={`${idPrefix}-summary`} title="Resumo com IA" icon="sparkles" defaultOpen>
          <section aria-label="Resumo por IA">
            <ConversationSummary conversationId={conversationId} target={target} />
          </section>
        </CollapsibleSection>
      )}
      {can('conversation.read') && (
        <CollapsibleSection id={`${idPrefix}-routing`} title="Roteamento e SLA" icon="clock" defaultOpen>
          <RoutingPanel conversationId={conversationId} target={target} />
        </CollapsibleSection>
      )}
      {contactId && can('contact.note') && (
        <CollapsibleSection id={`${idPrefix}-notes`} title="Anotações internas" icon="tickets" defaultOpen>
          <section aria-label="Anotações">
            <ContactNotes contactId={contactId} target={target} />
          </section>
        </CollapsibleSection>
      )}
      {can('attendance.history') && (
        <CollapsibleSection id={`${idPrefix}-history`} title="Histórico do contato" icon="conversations" defaultOpen>
          <div>
            {can('conversation.read') && <HistorySearch conversationId={conversationId} target={target} />}
            <AttendanceMemoryPanel conversationId={conversationId} target={target} canResolve={can('followup.resolve')} />
          </div>
        </CollapsibleSection>
      )}
    </>
  )
}
