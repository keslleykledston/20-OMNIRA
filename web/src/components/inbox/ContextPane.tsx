import React, { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import clsx from 'clsx';
import { ConversationItem } from '../../types/api';
import { API_BASE } from '../../lib/config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session';
import { Icon } from '../primitives';
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
import HistorySearch from './HistorySearch';
import FinalizeDialog from './FinalizeDialog';
import { CollapsibleSection } from './CollapsibleSection';
import { useChannelLines } from '../../lib/channelLines';
import { currentUserId } from '../../lib/session';

interface ContextPaneProps {
  conversationId: string;
  /** Show another conversation (opened/found on a different line). */
  onOpenConversation?: (conversationId: string) => void;
}

export default function ContextPane({ conversationId, onOpenConversation }: ContextPaneProps) {
  const tenantId = getTenantId();
  const queryClient = useQueryClient();
  const [assignLoading, setAssignLoading] = useState(false);
  const [assignError, setAssignError] = useState<string | null>(null);
  const [showTransferModal, setShowTransferModal] = useState(false);
  const [showFinalize, setShowFinalize] = useState(false);

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
    } catch (err: any) {
      if (isUnauthorized(err)) handleUnauthorized();
      else setAssignError(describeAssignError(err, 'unassign'));
    } finally {
      setAssignLoading(false);
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

  const channelLines = useChannelLines();
  const line = (channelLines.data ?? []).find((l) => l.id === conversation?.channel_connection_id);
  const mine = !!conversation?.assigned_to_user_id && conversation.assigned_to_user_id === currentUserId();
  const closed = conversation?.status === 'closed';
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
  };

  return (
    <div className="flex flex-col h-full bg-surface-muted overflow-y-auto">
      <header className="shrink-0 border-b border-border-subtle bg-surface px-4 py-3">
        <h2 className="text-sm font-semibold text-text-primary">Detalhes do atendimento</h2>
        <p className="mt-0.5 text-[11px] text-text-tertiary">Contato e contexto em um só lugar</p>
      </header>

      <CollapsibleSection id="attendance" title="Atendimento" icon="conversations" defaultOpen>
        <div className="space-y-2 px-4 pb-4 text-sm">
          <div className="flex items-center justify-between gap-3">
            <span className="text-text-secondary">Estado</span>
            <span
              className={clsx(
                'rounded-pill px-2 py-0.5 text-xs font-medium',
                closed ? 'bg-status-muted text-text-secondary' : 'bg-status-success-soft text-status-success',
              )}
            >
              {closed ? 'Finalizado' : 'Em atendimento'}
            </span>
          </div>
          <div className="flex items-center justify-between gap-3">
            <span className="text-text-secondary">Responsável</span>
            <span className="text-right font-medium text-text-primary">
              {!conversation?.assigned_to_user_id ? 'Sem responsável' : mine ? 'Você' : 'Outro atendente'}
            </span>
          </div>
          {line && (
            <div className="flex items-center justify-between gap-3">
              <span className="text-text-secondary">Canal</span>
              <span className="min-w-0 truncate text-right font-medium text-text-primary">{line.label}</span>
            </div>
          )}
          <div className="flex items-center justify-between gap-3">
            <span className="text-text-secondary">Mensagens</span>
            <span className="font-medium text-text-primary">{conversation?.message_count ?? '—'}</span>
          </div>
          {conversation?.created_at && (
            <div className="flex items-center justify-between gap-3">
              <span className="text-text-secondary">Iniciado em</span>
              <span className="font-medium text-text-primary">
                {new Date(conversation.created_at).toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })}
              </span>
            </div>
          )}
          {closed && (
            <p role="status" className="pt-1 text-xs text-text-secondary">
              Atendimento finalizado.
            </p>
          )}
          {assignError && (
            <div role="alert" className="rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">
              {assignError}
            </div>
          )}
          {/* A spam contact's conversation is not for attending: restore it first (ADR-0014). */}
          {canAct && (
            <div className="grid grid-cols-2 gap-2 pt-1">
              {conversation?.assigned_to_user_id ? (
                <button
                  onClick={handleUnassign}
                  disabled={assignLoading}
                  className={clsx(
                    'rounded-control border border-border-subtle bg-status-danger-soft px-3 py-2 text-sm font-medium text-status-danger hover:bg-status-danger-border',
                    assignLoading && 'cursor-not-allowed opacity-50',
                  )}
                >
                  Soltar
                </button>
              ) : (
                <button
                  onClick={handleAssign}
                  disabled={assignLoading}
                  className={clsx(
                    'rounded-control border border-border-subtle bg-accent-primary px-3 py-2 text-sm font-medium text-white hover:bg-accent-primary-hover',
                    assignLoading && 'cursor-not-allowed opacity-50',
                  )}
                >
                  Assumir
                </button>
              )}
              <button
                onClick={handleTransfer}
                disabled={assignLoading || !conversation?.assigned_to_user_id}
                className={clsx(
                  'rounded-control border border-border-subtle px-3 py-2 text-sm font-medium',
                  conversation?.assigned_to_user_id ? 'bg-surface text-text-primary hover:bg-surface-muted' : 'cursor-not-allowed bg-surface-muted text-text-tertiary',
                  assignLoading && 'opacity-50',
                )}
              >
                Transferir
              </button>
              {!internal && (
                <button
                  onClick={() => setShowFinalize(true)}
                  disabled={assignLoading}
                  className={clsx(
                    'col-span-2 rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm font-medium text-text-primary hover:bg-surface-muted',
                    assignLoading && 'cursor-not-allowed opacity-50',
                  )}
                >
                  Finalizar atendimento
                </button>
              )}
            </div>
          )}
        </div>
      </CollapsibleSection>

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
            contactName={conversation.contact_name}
            onChanged={refreshAll}
          />
        )}
      </CollapsibleSection>

      {conversation?.contact_id && onOpenConversation && (
        <CollapsibleSection id="channel" title="Canal" icon="channels" defaultOpen>
          <ChannelSwitcher
            contactId={conversation.contact_id}
            currentChannelId={conversation.channel_connection_id}
            onOpenConversation={onOpenConversation}
          />
        </CollapsibleSection>
      )}

      {/* ADR-0020: o que aconteceu antes com este contato e o que ficou pendente ou prometido */}
      {!internal && <AttendanceMemoryPanel conversationId={conversationId} />}
      {!internal && <HistorySearch conversationId={conversationId} />}

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

      {/* Chamado + atividade CRM */}
      <CollapsibleSection id="ticket" title="Chamado" icon="tickets" defaultOpen>
        <TicketPanel
          conversationId={conversationId}
          crmContactId={conversation?.crm_contact_id}
          conversationUnassigned={Boolean(conversation) && !conversation?.assigned_to_user_id}
        />
      </CollapsibleSection>

      <FinalizeDialog
        open={showFinalize}
        conversationId={conversationId}
        contactName={conversation?.contact_name}
        onClose={() => setShowFinalize(false)}
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

function describeAssignError(err: any, action: 'assign' | 'unassign' | 'transfer' = 'assign'): string {
  const status = err?.response?.status;
  const body = typeof err?.response?.data === 'string' ? err.response.data : '';
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
    return body || 'Operador não elegível para esta ação.';
  }
  const messages = {
    assign: 'Erro ao assumir conversa.',
    unassign: 'Erro ao soltar conversa.',
    transfer: 'Erro ao transferir conversa.'
  };
  return body || messages[action];
}
