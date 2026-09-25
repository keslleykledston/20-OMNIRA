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

interface ContextPaneProps {
  conversationId: string;
}

export default function ContextPane({ conversationId }: ContextPaneProps) {
  const tenantId = getTenantId();
  const queryClient = useQueryClient();
  const [assignLoading, setAssignLoading] = useState(false);
  const [assignError, setAssignError] = useState<string | null>(null);
  const [showTransferModal, setShowTransferModal] = useState(false);

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

  return (
    <div className="flex flex-col h-full bg-surface-muted overflow-y-auto">
      {/* Contact Card. PRODUCT.7B1C: the "Ver perfil 360°" link that used
          to sit here was removed — it pointed crm_contact_id (a K3G
          identity) at /contacts/:id (an OMNIRA-internal contact id), two
          different identity domains, and the app uses BrowserRouter so
          its #/contacts/... hash fragment never resolved to anything. */}
      <div className="p-4 border-b border-border-subtle">
        <h4 className="text-xs font-semibold text-text-tertiary mb-3 uppercase">Contato</h4>
        <div className="flex gap-3 items-start">
          <div className="w-12 h-12 rounded-full bg-accent-primary text-white flex items-center justify-center flex-shrink-0 font-semibold text-lg">
            {conversation?.contact_name?.[0]?.toUpperCase() || '?'}
          </div>
          <div className="flex-1 min-w-0">
            <h5 className="font-semibold text-text-primary truncate">
              {conversation?.contact_name}
            </h5>
            <p className="text-sm text-text-secondary break-words">
              {conversation?.contact_phone}
            </p>
          </div>
        </div>
      </div>

      {/* Conversation Stats */}
      <div className="p-4 border-b border-border-subtle">
        <h4 className="text-xs font-semibold text-text-tertiary mb-3 uppercase">Conversa</h4>
        <div className="space-y-2 text-sm">
          <div className="flex justify-between items-center">
            <span className="text-text-secondary">Mensagens</span>
            <span className="font-semibold text-text-primary">{conversation?.message_count || 0}</span>
          </div>
          <div className="flex justify-between items-center">
            <span className="text-text-secondary">Status</span>
            <span className={clsx(
              'text-xs font-medium px-2 py-0.5 rounded-pill',
              conversation?.status === 'active' ? 'bg-status-success-soft text-status-success' :
              conversation?.status === 'closed' ? 'bg-status-muted text-text-secondary' :
              'bg-status-warning-soft text-status-warning'
            )}>
              {conversation?.status === 'active' ? 'Ativo' : conversation?.status === 'closed' ? 'Fechado' : 'Pendente'}
            </span>
          </div>
        </div>
      </div>

      {/* Assignment */}
      {conversation?.assigned_to_user_id && (
        <div className="p-4 border-b border-border-subtle">
          <h4 className="text-xs font-semibold text-text-tertiary mb-2 uppercase">Atendimento</h4>
          <div className="text-sm">
            <span className="inline-flex rounded-md bg-accent-primary-soft px-2 py-1 text-xs font-medium text-accent-primary">
              Atribuído a você
            </span>
          </div>
        </div>
      )}

      {/* PRODUCT.7B1C: a "Participantes" section used to render here from
          conversation.participants, but the canonical Inbox conversation
          read (internal/inbox/adapters/http.go GetConversation/
          ListConversations) never selects or joins that field — it was
          always undefined here, so the section could never actually
          render. Removed rather than left as unreachable dead code.
          External/group participant modeling is future scope. */}

      {/* Chamado + atividade CRM */}
      <TicketPanel conversationId={conversationId} crmContactId={conversation?.crm_contact_id} />

      {/* Error */}
      {assignError && (
        <div className="p-4">
          <div role="alert" className="p-2 bg-status-danger-soft text-status-danger text-xs rounded-control">
            {assignError}
          </div>
        </div>
      )}

      {/* Actions */}
      <div className="p-4 space-y-2">
        {conversation?.assigned_to_user_id ? (
          <button
            onClick={handleUnassign}
            disabled={assignLoading}
            className={clsx(
              'w-full px-3 py-2 text-sm font-medium rounded-control transition-colors',
              'bg-status-danger-soft text-status-danger hover:bg-status-danger-border',
              'border border-border-subtle',
              assignLoading && 'opacity-50 cursor-not-allowed'
            )}
          >
            <Icon name="check" className="w-4 h-4 mr-2" />
            Soltar
          </button>
        ) : (
          <button
            onClick={handleAssign}
            disabled={assignLoading}
            className={clsx(
              'w-full px-3 py-2 text-sm font-medium rounded-control transition-colors',
              'bg-accent-primary text-white hover:bg-accent-primary-hover',
              'border border-border-subtle',
              assignLoading && 'opacity-50 cursor-not-allowed'
            )}
          >
            <Icon name="plus" className="w-4 h-4 mr-2" />
            Assumir
          </button>
        )}
        <button
          onClick={handleTransfer}
          disabled={assignLoading || !conversation?.assigned_to_user_id}
          className={clsx(
            'w-full px-3 py-2 text-sm font-medium rounded-control transition-colors',
            conversation?.assigned_to_user_id
              ? 'bg-surface text-text-primary hover:bg-surface-muted'
              : 'bg-surface-muted text-text-tertiary cursor-not-allowed',
            'border border-border-subtle',
            assignLoading && 'opacity-50 cursor-not-allowed'
          )}
        >
          <Icon name="info" className="w-4 h-4 mr-2" />
          Transferir
        </button>
      </div>

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
