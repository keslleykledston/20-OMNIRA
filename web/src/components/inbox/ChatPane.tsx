import React, { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import clsx from 'clsx';
import { MessageItem, ConversationItem } from '../../types/api';
import { API_BASE } from '../../lib/config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session';
import { useRealtimeEvents } from '../../hooks/useRealtimeEvents';
import MessageBubble from './MessageBubble';
import MessageComposer from './MessageComposer';
import { Icon } from '../primitives';

interface ChatPaneProps {
  conversationId: string;
  onBack?: () => void;
  onToggleContext?: () => void;
}

export default function ChatPane({ conversationId, onBack, onToggleContext }: ChatPaneProps) {
  const tenantId = getTenantId();
  const queryClient = useQueryClient();
  const timelineEndRef = useRef<HTMLDivElement>(null);
  const [messages, setMessages] = useState<MessageItem[]>([]);
  const [conversation, setConversation] = useState<ConversationItem | null>(null);
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);

  // Fetch conversation details
  const { data: conversationData } = useQuery({
    queryKey: ['inbox-conversation-detail', tenantId, conversationId],
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

  // Fetch messages
  const { data: messagesData } = useQuery({
    queryKey: ['inbox-messages', tenantId, conversationId],
    queryFn: async () => {
      try {
        const res = await axios.get(
          `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/messages`,
          { params: { limit: 50 }, headers: authHeaders() }
        );
        return res.data;
      } catch (err) {
        if (isUnauthorized(err)) handleUnauthorized();
        throw err;
      }
    },
    enabled: !!tenantId && !!conversationId,
  });

  useEffect(() => {
    if (conversationData) setConversation(conversationData);
  }, [conversationData]);

  useEffect(() => {
    if (messagesData?.items) {
      setMessages(messagesData.items);
    }
  }, [messagesData]);

  // Realtime events
  useRealtimeEvents({
    tenantId,
    conversationId,
    onEvent: () => {
      void queryClient.invalidateQueries({ queryKey: ['inbox-messages', tenantId, conversationId] });
      void queryClient.invalidateQueries({ queryKey: ['inbox-conversation-detail', tenantId, conversationId] });
    },
  });

  // Auto-scroll to bottom
  useEffect(() => {
    timelineEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  const handleSendMessage = async (text: string) => {
    if (!text.trim()) return;

    setSending(true);
    setSendError(null);

    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/messages`,
        { body: text },
        { headers: authHeaders() }
      );
      // Reset and refetch
      void queryClient.invalidateQueries({ queryKey: ['inbox-messages', tenantId, conversationId] });
    } catch (err: any) {
      if (isUnauthorized(err)) handleUnauthorized();
      else setSendError(err?.response?.data?.message || 'Erro ao enviar');
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="flex flex-col h-full bg-surface">
      {/* Header */}
      <div className="flex items-center justify-between p-4 border-b border-border-subtle">
        <div className="flex items-center gap-3 flex-1 min-w-0">
          {onBack && (
            <button
              onClick={onBack}
              className="lg:hidden p-1 hover:bg-surface-muted rounded-control"
              title="Voltar"
            >
              <Icon name="arrow-left" />
            </button>
          )}
          <div className="min-w-0">
            <h3 className="font-semibold text-text-primary truncate">
              {conversation?.contact_name}
            </h3>
            <p className="text-xs text-text-secondary">
              {conversation?.contact_phone}
            </p>
          </div>
        </div>

        {/* Actions */}
        <div className="flex gap-2">
          {onToggleContext && (
            <button
              onClick={onToggleContext}
              className="p-2 hover:bg-surface-muted rounded-control"
              title="Contexto"
            >
              <Icon name="info" />
            </button>
          )}
          <button
            className="p-2 hover:bg-surface-muted rounded-control"
            title="Menu"
          >
            <Icon name="more" />
          </button>
        </div>
      </div>

      {/* Timeline */}
      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        {messages.length === 0 ? (
          <div className="flex items-center justify-center h-full text-text-tertiary text-sm">
            Nenhuma mensagem
          </div>
        ) : (
          messages.map((msg) => (
            <MessageBubble key={msg.id} message={msg} />
          ))
        )}
        <div ref={timelineEndRef} />
      </div>

      {/* Composer */}
      <div className="p-4 border-t border-border-subtle">
        {sendError && (
          <div className="mb-3 p-2 bg-status-danger-soft text-status-danger text-xs rounded-control">
            {sendError}
          </div>
        )}
        <MessageComposer
          onSend={handleSendMessage}
          disabled={sending}
          placeholder="Escreva uma resposta..."
        />
      </div>
    </div>
  );
}
