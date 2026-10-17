import React, { useId, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import clsx from 'clsx';
import { ConversationItem } from '../../types/api';
import { API_BASE } from '../../lib/config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session';
import { ActionIcon, Icon } from '../primitives';
import { TicketPanel } from '../TicketPanel';
import { TechnicianSelectModal } from '../TechnicianSelectModal';
import ConversationSummary from './ConversationSummary';
import TopicsPanel from '../topics/TopicsPanel';
import { ContactKindControl } from '../contacts/ContactKindControl';
import { ContactDetailsEditor } from '../contacts/ContactDetailsEditor';
import { ContactNotes } from '../contacts/ContactNotes';
import { WhatsAppName } from '../contacts/WhatsAppName';
import { ChannelSwitcher } from './ChannelSwitcher';
import AttendanceMemoryPanel from './AttendanceMemoryPanel';
import RoutingPanel from './RoutingPanel';
import HistorySearch from './HistorySearch';
import FinalizeDialog from './FinalizeDialog';
import QueueSelectDialog from './QueueSelectDialog';
import { finalizedNotice } from '../../lib/attendance';
import { emitInboxNotice } from '../../lib/inboxNotice';
import { CollapsibleSection } from './CollapsibleSection';
import { useChannelLines } from '../../lib/channelLines';
import { classificationAPI } from '../../lib/contacts';
import { currentUserId } from '../../lib/session';
import { useAccess } from '../../lib/useAccess';
import ServiceSessionPanel from '../service/ServiceSessionPanel';

interface ContextPaneProps {
  conversationId: string;
  /** Show another conversation (opened/found on a different line). */
  onOpenConversation?: (conversationId: string) => void;
  /** The conversation was sent to another queue: it may now be outside this person's reach (the queue scope), so the screen lets go of it. */
  onSentToQueue?: () => void;
}

export default function ContextPane({ conversationId, onOpenConversation, onSentToQueue }: ContextPaneProps) {
  const tenantId = getTenantId();
  const queryClient = useQueryClient();
  const [assignLoading, setAssignLoading] = useState(false);
  const [assignError, setAssignError] = useState<string | null>(null);
  const [showTransferModal, setShowTransferModal] = useState(false);
  const [showQueueDialog, setShowQueueDialog] = useState(false);
  const [queueBusy, setQueueBusy] = useState(false);
  const [queueError, setQueueError] = useState<string | null>(null);
  const [showFinalize, setShowFinalize] = useState(false);
  const { can } = useAccess();
  const [tab, setTab] = useState<'details' | 'history'>('details');
  const tabsId = useId();

  const handleAssign = async () => {
    setAssignLoading(true);
    setAssignError(null);
    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/assign`,
        {},
        { headers: authHeaders() }
      );
      await queryClient.invalidateQueries({ queryKey: ['inbox-context', tenantId, conversationId] });
      await queryClient.invalidateQueries({ queryKey: ['inbox-conversation-detail', tenantId, conversationId] });
      void queryClient.invalidateQueries({ queryKey: ['service-session'] }); // assigning or finishing may open/close the protocol
    } catch (err: any) {
      if (isUnauthorized(err)) handleUnauthorized();
      else setAssignError(describeAssignError(err, 'assign'));
    } finally {
      setAssignLoading(false);
    }
  };

  const handleUnassign = async () => {
    setAssignLoading(true);
    setAssignError(null);
    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/unassign`,
        {},
        { headers: authHeaders() }
      );
      await queryClient.invalidateQueries({ queryKey: ['inbox-context', tenantId, conversationId] });
      await queryClient.invalidateQueries({ queryKey: ['inbox-conversation-detail', tenantId, conversationId] });
      void queryClient.invalidateQueries({ queryKey: ['service-session'] }); // assigning or finishing may open/close the protocol
    } catch (err: any) {
      if (isUnauthorized(err)) handleUnauthorized();
      else setAssignError(describeAssignError(err, 'unassign'));
    } finally {
      setAssignLoading(false);
    }
  };

  // send the conversation to another queue (department): the queue's rules distribute it; whoever holds it (you) is released
  const handleSendToQueue = async (queueId: string, queueName: string) => {
    setQueueBusy(true);
    setQueueError(null);
    try {
      await axios.post(`${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/route-queue`, { queue_id: queueId }, { headers: authHeaders() });
      setShowQueueDialog(false);
      void queryClient.invalidateQueries({ queryKey: ['inbox-context', tenantId, conversationId] });
      void queryClient.invalidateQueries({ queryKey: ['inbox-conversation-detail', tenantId, conversationId] });
      void queryClient.invalidateQueries({ queryKey: ['conv-routing'] });
      void queryClient.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] });
      emitInboxNotice(conversationId, `Conversa enviada para a fila ${queueName}.`);
      onSentToQueue?.();
    } catch (err: any) {
      if (isUnauthorized(err)) handleUnauthorized();
      else setQueueError(describeAssignError(err, 'route'));
    } finally {
      setQueueBusy(false);
    }
  };

  const handleTransfer = async () => {
    if (!conversation?.assigned_to_user_id) {
      setAssignError('Conversa não atribuída. Assuma antes de transferir.');
      return;
    }
    setShowTransferModal(true);
  };

  const handleTransferToTechnician = async (technicianId: string) => {
    setAssignLoading(true);
    setAssignError(null);
    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/transfer`,
        { assignee_user_id: technicianId },
        { headers: authHeaders() }
      );
      await queryClient.invalidateQueries({ queryKey: ['inbox-context', tenantId, conversationId] });
      await queryClient.invalidateQueries({ queryKey: ['inbox-conversation-detail', tenantId, conversationId] });
      void queryClient.invalidateQueries({ queryKey: ['service-session'] }); // assigning or finishing may open/close the protocol
      setShowTransferModal(false);
    } catch (err: any) {
      if (isUnauthorized(err)) handleUnauthorized();
      else setAssignError(describeAssignError(err, 'transfer'));
      throw err;
    } finally {
      setAssignLoading(false);
    }
  };

  // Fetch conversation (which contains contact info)
  const { data: conversation } = useQuery({
    queryKey: ['inbox-context', tenantId, conversationId],
    queryFn: async () => {
      try {
        const res = await axios.get(
          `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}`,
          { headers: authHeaders() }
        );
        return res.data as ConversationItem;
      } catch (err) {
        if (isUnauthorized(err)) handleUnauthorized();
        throw err;
      }
    },
    enabled: !!tenantId && !!conversationId,
  });

  // The companies the contact belongs to (primary first): the ticket form offers only those when it can tell which ones they are.
  const links = useQuery({
    queryKey: ['contact-classification', tenantId, conversation?.contact_id],
    queryFn: () => classificationAPI.get(conversation!.contact_id!),
    enabled: !!tenantId && !!conversation?.contact_id,
    retry: false,
  });
  const contactCompanyNames = [...(links.data?.accounts ?? [])]
    .filter((l) => l.status === 'active')
    .sort((a, b) => Number(b.primary) - Number(a.primary))
    .map((l) => l.account_name);
  const channelLines = useChannelLines();
  // The line this conversation runs on is already in the list and the chat; the section only exists to reach the person through ANOTHER line.
  const otherLines = (channelLines.data ?? []).filter((l) => l.id !== conversation?.channel_connection_id && l.can_send_text);
  const mine = !!conversation?.assigned_to_user_id && conversation.assigned_to_user_id === currentUserId();
  const closed = conversation?.status === 'closed';
  const ownerLabel = !conversation?.assigned_to_user_id
    ? 'Sem responsável'
    : mine
      ? conversation.assigned_to_name ? `${conversation.assigned_to_name} (você)` : 'Você'
      : conversation.assigned_to_name || 'Outro atendente';
  const internal = conversation?.conversation_kind === 'internal';
  const canAct = conversation?.contact_kind !== 'spam' && !closed;
  const [copied, setCopied] = useState(false);
  const copyPhone = async () => {
    try {
      await navigator.clipboard.writeText(conversation?.contact_phone ?? '');
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      /* the number is selectable text; copying by hand still works */
    }
  };
  const refreshAll = () => {
    void queryClient.invalidateQueries({ queryKey: ['inbox-context', tenantId, conversationId] });
    void queryClient.invalidateQueries({ queryKey: ['inbox-conversation-detail', tenantId, conversationId] });
    void queryClient.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] });
    void queryClient.invalidateQueries({ queryKey: ['contact-classification', tenantId] });
  };

  return (
    <div className="flex flex-col h-full bg-surface-muted overflow-y-auto">
      <header className="shrink-0 border-b border-border-subtle bg-surface px-4 py-3">
        <h2 className="text-sm font-semibold text-text-primary">Detalhes do atendimento</h2>
      </header>

      <div role="tablist" aria-label="Contexto do atendimento" className="grid h-10 flex-shrink-0 grid-cols-2 border-b border-border-subtle bg-surface px-4">
        {([['details', 'Detalhes'], ['history', 'Histórico']] as const).map(([value, label]) => (
          <button
            key={value}
            type="button"
            role="tab"
            id={`${tabsId}-tab-${value}`}
            aria-selected={tab === value}
            aria-controls={`${tabsId}-panel-${value}`}
            tabIndex={tab === value ? 0 : -1}
            onClick={() => setTab(value)}
            onKeyDown={(e) => {
              if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
              e.preventDefault();
              const next = tab === 'details' ? 'history' : 'details';
              setTab(next);
              document.getElementById(`${tabsId}-tab-${next}`)?.focus();
            }}
            className={clsx(
              'border-b-2 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent-primary',
              tab === value ? 'border-accent-primary text-accent-primary' : 'border-transparent text-text-secondary hover:text-text-primary'
            )}
          >
            {label}
          </button>
        ))}
      </div>

      {tab === 'history' && (
        <div role="tabpanel" id={`${tabsId}-panel-history`} aria-labelledby={`${tabsId}-tab-history`} tabIndex={0} className="min-h-0 flex-1 overflow-y-auto focus-visible:outline-none">
          <div className="p-4">
            <h3 className="mb-3 text-[10px] font-semibold uppercase text-text-tertiary">Histórico deste atendimento</h3>
            <ol className="m-0 list-none space-y-4 border-l border-border-subtle pl-4">
              <li>
                <p className="text-sm font-medium text-text-primary">Conversa iniciada</p>
                <p className="mt-0.5 text-xs text-text-secondary">
                  {conversation?.created_at ? new Date(conversation.created_at).toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'Horário não informado'}
                </p>
              </li>
              <li>
                <p className="text-sm font-medium text-text-primary">Responsável atual</p>
                <p className="mt-0.5 text-xs text-text-secondary">{ownerLabel}</p>
              </li>
              {conversation?.ticket_status && (
                <li>
                  <p className="text-sm font-medium text-text-primary">Chamado aberto</p>
                  <p className="mt-0.5 text-xs text-text-secondary">Situação: {conversation.ticket_status}</p>
                </li>
              )}
              {closed && (
                <li>
                  <p className="text-sm font-medium text-text-primary">Atendimento encerrado</p>
                  <p className="mt-0.5 text-xs text-text-secondary">O histórico foi preservado.</p>
                </li>
              )}
            </ol>
          </div>
          {/* ADR-0020: what happened before with this contact, and what was left pending or promised */}
          {!internal && <AttendanceMemoryPanel conversationId={conversationId} />}
          {!internal && <HistorySearch conversationId={conversationId} />}
        </div>
      )}

      <div role="tabpanel" id={`${tabsId}-panel-details`} aria-labelledby={`${tabsId}-tab-details`} hidden={tab !== 'details'} className={clsx(tab === 'details' && 'min-h-0 flex-1')}>
      {/* The state of the attendance in two lines (who holds it, since when) and the three things you do to it as icons: the labels are the tooltips and the accessible names. */}
      <section aria-label="Atendimento" className="space-y-1.5 border-b border-border-subtle px-4 py-3">
        <div className="flex items-center justify-between gap-2">
          <p className="flex min-w-0 items-center gap-2 text-sm">
            <span
              className={clsx('inline-flex flex-shrink-0 items-center gap-1.5 rounded-pill px-2 py-0.5 text-xs font-medium', closed ? 'bg-status-muted text-text-secondary' : 'bg-status-success-soft text-status-success')}
            >
              <span aria-hidden="true" className={clsx('h-1.5 w-1.5 rounded-full', closed ? 'bg-text-tertiary' : 'bg-status-success')} />
              {closed ? 'Finalizado' : 'Em atendimento'}
            </span>
            <span className="min-w-0 truncate font-medium text-text-primary" title={`Responsável: ${ownerLabel}`}>{ownerLabel}</span>
          </p>
          {/* A spam contact's conversation is not for attending: restore it first (ADR-0014). */}
          {canAct && (
            <div className="flex flex-shrink-0 items-center gap-1.5">
              {conversation?.assigned_to_user_id ? (
                <ActionIcon icon="user-minus" label="Soltar" tone="danger" onClick={handleUnassign} disabled={assignLoading} />
              ) : (
                <ActionIcon icon="user-plus" label="Assumir" tone="primary" onClick={handleAssign} disabled={assignLoading} />
              )}
              <ActionIcon icon="transfer" label="Transferir" onClick={handleTransfer} disabled={assignLoading || !conversation?.assigned_to_user_id} />
              {!internal && conversation?.status !== 'closed' && <ActionIcon icon="building" label="Enviar para uma fila" onClick={() => { setQueueError(null); setShowQueueDialog(true) }} disabled={assignLoading} />}
              {!internal && <ActionIcon icon="check-circle" label="Finalizar atendimento" onClick={() => setShowFinalize(true)} disabled={assignLoading} />}
            </div>
          )}
        </div>
        {closed ? (
          <p role="status" className="text-[11px] text-text-tertiary">Atendimento finalizado.</p>
        ) : (
          <p className="text-[11px] text-text-tertiary">
            {conversation?.created_at ? `Desde ${new Date(conversation.created_at).toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })}` : ''}
            {conversation?.message_count != null && ` · ${conversation.message_count} ${conversation.message_count === 1 ? 'mensagem' : 'mensagens'}`}
          </p>
        )}
        {assignError && (
          <div role="alert" className="rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">
            {assignError}
          </div>
        )}
      </section>

      {!internal && (
        <CollapsibleSection id="service-session" title="Protocolo e tarefas" icon="tickets" defaultOpen>
          <ServiceSessionPanel conversationId={conversationId} can={can} conversationClosed={closed} />
        </CollapsibleSection>
      )}

      <CollapsibleSection id="contact" title="Contato" icon="contacts" defaultOpen>
        {/* PRODUCT.7B1C: the "Ver perfil 360°" link was removed: it pointed crm_contact_id (a K3G identity) at /contacts/:id
            (an OMNIRA-internal contact id), two different identity domains. */}
        <div className="px-4 pb-3">
          <div className="flex items-start gap-3">
            <div className="flex h-11 w-11 flex-shrink-0 items-center justify-center rounded-full bg-accent-primary text-base font-semibold text-white">
              {conversation?.contact_name?.[0]?.toUpperCase() || '?'}
            </div>
            <div className="min-w-0 flex-1">
              <h5 className="truncate font-semibold text-text-primary">{conversation?.contact_name}</h5>
              <WhatsAppName principal={conversation?.contact_name} whatsapp={conversation?.contact_whatsapp_name} />
              <div className="flex items-center gap-1">
                <p className="min-w-0 break-words text-sm text-text-secondary">{conversation?.contact_phone}</p>
                {conversation?.contact_phone && (
                  <button
                    type="button"
                    onClick={() => void copyPhone()}
                    aria-label={copied ? 'Telefone copiado' : 'Copiar telefone'}
                    title={copied ? 'Copiado' : 'Copiar telefone'}
                    className="flex-shrink-0 rounded-control p-1 text-text-tertiary hover:bg-surface hover:text-text-primary"
                  >
                    <Icon name={copied ? 'check' : 'copy'} size={14} />
                  </button>
                )}
              </div>
            </div>
          </div>
        </div>
        {conversation?.contact_id && <ContactDetailsEditor contactId={conversation.contact_id} onChanged={refreshAll} />}
        {/* ADR-0014: who this contact is (customer / other / spam). Reclassifying changes which Inbox list the
            contact's conversations belong to, so the lists and this pane are refreshed. */}
        {conversation?.contact_id && (
          <ContactKindControl
            contactId={conversation.contact_id}
            kind={conversation.contact_kind || 'unclassified'}
            internalRole={conversation.contact_internal_role}
            contactName={conversation.contact_name}
            onChanged={refreshAll}
          />
        )}
      </CollapsibleSection>

      {conversation?.contact_id && onOpenConversation && otherLines.length > 0 && (
        <CollapsibleSection id="channel" title="Outros canais" icon="channels">
          <ChannelSwitcher
            contactId={conversation.contact_id}
            currentChannelId={conversation.channel_connection_id}
            onOpenConversation={onOpenConversation}
          />
        </CollapsibleSection>
      )}

      {conversation?.contact_id && (
        <CollapsibleSection id="notes" title="Anotações internas" icon="tickets">
          <ContactNotes contactId={conversation.contact_id} />
        </CollapsibleSection>
      )}

      {/* PRODUCT.7C1: on-demand, non-persisted AI conversation summary */}
      <CollapsibleSection id="summary" title="Resumo com IA" icon="sparkles">
        <ConversationSummary conversationId={conversationId} />
      </CollapsibleSection>

      <CollapsibleSection id="topics" title="Assuntos" icon="conversations">
        <TopicsPanel conversationId={conversationId} />
      </CollapsibleSection>

      {/* ADR-0044: setor, fila, espera, SLA e histórico de roteamento */}
      {!internal && (
        <CollapsibleSection id="routing" title="Roteamento e SLA" icon="clock">
          <RoutingPanel conversationId={conversationId} />
        </CollapsibleSection>
      )}

      {/* Chamado + atividade CRM */}
      <CollapsibleSection id="ticket" title="Chamado" icon="tickets" defaultOpen>
        <TicketPanel
          conversationId={conversationId}
          crmContactId={conversation?.crm_contact_id}
          conversationUnassigned={Boolean(conversation) && !conversation?.assigned_to_user_id}
          contactCompanyNames={contactCompanyNames}
        />
      </CollapsibleSection>
      </div>

      <FinalizeDialog
        open={showFinalize}
        conversationId={conversationId}
        contactName={conversation?.contact_name}
        onClose={() => setShowFinalize(false)}
        onFinalized={(farewell) => emitInboxNotice(conversationId, finalizedNotice(farewell))}
      />

      <QueueSelectDialog
        open={showQueueDialog}
        currentQueueId={conversation?.queue_id}
        busy={queueBusy}
        error={queueError}
        onSend={(id, name) => void handleSendToQueue(id, name)}
        onClose={() => setShowQueueDialog(false)}
      />

      {/* Transfer Modal */}
      <TechnicianSelectModal
        isOpen={showTransferModal}
        title="Transferir para um técnico"
        onSelect={handleTransferToTechnician}
        onClose={() => setShowTransferModal(false)}
        excludeUserIds={conversation?.assigned_to_user_id ? [conversation.assigned_to_user_id] : []}
      />
    </div>
  );
}

function describeAssignError(err: any, action: 'assign' | 'unassign' | 'transfer' | 'route' = 'assign'): string {
  const status = err?.response?.status;
  const body = typeof err?.response?.data === 'string' ? err.response.data : '';
  if (action === 'route') {
    if (status === 403) return 'Você não pode enviar esta conversa: ela está com outra pessoa ou você não tem permissão.';
    if (status === 409) return 'Esta conversa não pode ir para uma fila agora: está encerrada, é uma conversa interna ou está com uma aplicação externa.';
    if (status === 422) return 'Essa fila não existe mais ou o setor dela está desligado. Escolha outra.';
    if (status === 404) return 'Conversa não encontrada.';
    return 'Não foi possível enviar para a fila. Tente de novo.';
  }
  if (status === 409) {
    return 'Este atendimento acabou de ser modificado por outro operador.';
  }
  if (status === 403) {
    return 'Você não tem permissão para esta ação.';
  }
  if (status === 404) {
    return 'Conversa ou operador não encontrado.';
  }
  if (status === 422) {
    return action === 'assign' && (/not an eligible agent/i.test(body) || !body)
      ? 'Você não é atendente desta instância. Peça a um administrador para ativar "Atende" na sua ficha, em Pessoas.'
      : body || 'Operador não elegível para esta ação.';
  }
  const messages = {
    assign: 'Erro ao assumir conversa.',
    unassign: 'Erro ao soltar conversa.',
    transfer: 'Erro ao transferir conversa.',
    route: 'Erro ao enviar para a fila.'
  };
  return body || messages[action];
}
