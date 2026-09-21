import React, { useState, useCallback, useEffect } from 'react';
import { useQuery } from '@tanstack/react-query';
import axios from 'axios';
import { API_BASE } from '../lib/config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../lib/session';
import { ConversationItem } from '../types/api';
import ConversationListPanel from '../components/inbox/ConversationListPanel';
import ChatPane from '../components/inbox/ChatPane';
import ContextPane from '../components/inbox/ContextPane';

/**
 * InboxWorkspace — workspace 3-painel
 * Desktop: ConversationList (~350px) | ChatPane (flex) | ContextPane (~320px)
 * Tablet: ConversationList + ChatPane; context em sheet
 * Mobile: ConversationList → ChatPane; context em sheet
 */
export default function InboxWorkspace() {
  const tenantId = getTenantId();
  const [selectedConversationId, setSelectedConversationId] = useState<string | null>(null);
  const [segment, setSegment] = useState<'all' | 'unread' | 'mine'>('all');
  const [showContext, setShowContext] = useState(true);

  // Fetch conversation list
  const { data: conversationsData, isLoading: listLoading } = useQuery({
    queryKey: ['inbox-conversations', tenantId, segment],
    queryFn: async () => {
      try {
        const res = await axios.get(
          `${API_BASE}/tenants/${tenantId}/inbox/conversations`,
          { params: { limit: 50, status: segment === 'all' ? undefined : segment }, headers: authHeaders() }
        );
        return res.data;
      } catch (err) {
        if (isUnauthorized(err)) handleUnauthorized();
        throw err;
      }
    },
    enabled: !!tenantId,
  });

  const conversations = conversationsData?.items || [];

  // Auto-select first conversation if none selected
  useEffect(() => {
    if (!selectedConversationId && conversations.length > 0) {
      setSelectedConversationId(conversations[0].id);
    }
  }, [conversations, selectedConversationId]);

  return (
    <div className="inbox-workspace">
      {/* Desktop: 3-painel */}
      <div className="hidden lg:grid lg:grid-cols-4 lg:gap-0 h-[calc(100vh-theme(spacing.16))]">
        {/* ConversationList */}
        <div className="col-span-1 border-r border-border-subtle overflow-hidden flex flex-col">
          <ConversationListPanel
            conversations={conversations}
            selectedId={selectedConversationId}
            onSelect={setSelectedConversationId}
            segment={segment}
            onSegmentChange={setSegment}
            isLoading={listLoading}
          />
        </div>

        {/* ChatPane */}
        <div className="col-span-2 flex flex-col">
          {selectedConversationId ? (
            <ChatPane conversationId={selectedConversationId} />
          ) : (
            <div className="flex-1 flex items-center justify-center text-text-secondary">
              Selecione uma conversa
            </div>
          )}
        </div>

        {/* ContextPane */}
        {showContext && selectedConversationId && (
          <div className="col-span-1 border-l border-border-subtle overflow-hidden flex flex-col">
            <ContextPane conversationId={selectedConversationId} />
          </div>
        )}
      </div>

      {/* Tablet/Mobile: lista ou detalhe */}
      <div className="lg:hidden">
        {selectedConversationId ? (
          <ChatPane
            conversationId={selectedConversationId}
            onBack={() => setSelectedConversationId(null)}
            onToggleContext={() => setShowContext(!showContext)}
          />
        ) : (
          <ConversationListPanel
            conversations={conversations}
            selectedId={selectedConversationId}
            onSelect={setSelectedConversationId}
            segment={segment}
            onSegmentChange={setSegment}
            isLoading={listLoading}
          />
        )}
      </div>

      <style>{`
        .inbox-workspace {
          height: 100%;
          width: 100%;
          display: flex;
          flex-direction: column;
        }
      `}</style>
    </div>
  );
}
