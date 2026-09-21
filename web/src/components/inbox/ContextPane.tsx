import React from 'react';
import { useQuery } from '@tanstack/react-query';
import axios from 'axios';
import clsx from 'clsx';
import { ConversationItem } from '../../types/api';
import { API_BASE } from '../../lib/config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session';
import { Icon } from '../primitives';

interface ContextPaneProps {
  conversationId: string;
}

export default function ContextPane({ conversationId }: ContextPaneProps) {
  const tenantId = getTenantId();

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
      {/* Contact Card */}
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
            {conversation?.crm_contact_id && (
              <a
                href={`#/contacts/${conversation.crm_contact_id}`}
                className="text-xs text-accent-primary hover:underline mt-1 block"
              >
                Ver perfil 360°
              </a>
            )}
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
          {conversation?.assigned_to_user_id && (
            <div className="flex justify-between items-center">
              <span className="text-text-secondary">Atribuído</span>
              <span className="text-xs font-medium text-accent-primary">Sim</span>
            </div>
          )}
        </div>
      </div>

      {/* Actions */}
      <div className="p-4 space-y-2">
        <button className={clsx(
          'w-full px-3 py-2 text-sm font-medium rounded-control transition-colors',
          'bg-surface text-text-primary hover:bg-surface-muted',
          'border border-border-subtle'
        )}>
          <Icon name="plus" className="w-4 h-4 mr-2" />
          Transferir
        </button>
        <button className={clsx(
          'w-full px-3 py-2 text-sm font-medium rounded-control transition-colors',
          'bg-surface text-text-primary hover:bg-surface-muted',
          'border border-border-subtle'
        )}>
          <Icon name="check" className="w-4 h-4 mr-2" />
          Resolver
        </button>
        <button className={clsx(
          'w-full px-3 py-2 text-sm font-medium rounded-control transition-colors',
          'bg-surface text-text-primary hover:bg-surface-muted',
          'border border-border-subtle'
        )}>
          <Icon name="info" className="w-4 h-4 mr-2" />
          Tags
        </button>
      </div>
    </div>
  );
}
