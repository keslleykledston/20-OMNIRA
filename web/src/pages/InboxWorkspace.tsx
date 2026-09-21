import React, { useState, useEffect, useRef } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import clsx from 'clsx';
import { API_BASE } from '../lib/config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../lib/session';
import { useRealtimeEvents } from '../hooks/useRealtimeEvents';
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
  const queryClient = useQueryClient();
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

  // New conversations / assignment / status changes anywhere in the tenant:
  // refetch the list. Without this the list only ever updates on manual
  // reload — confirmed missing by a real E2E run (proven regression against
  // the previous InboxPage, which had this wired via the same hook).
  useRealtimeEvents({
    tenantId,
    onEvent: () => {
      void queryClient.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] });
    },
    onReconnect: () => {
      void queryClient.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] });
    },
  });

  // Auto-select the first conversation once, on initial load only — not every
  // time selection clears. Without the "only once" guard, pressing Back
  // (which sets selectedConversationId to null) got immediately overridden by
  // this same effect re-selecting conversations[0], making Back a no-op on
  // tablet/mobile (confirmed by a real browser E2E run, not a hypothetical).
  const autoSelectedRef = useRef(false);
  useEffect(() => {
    if (!autoSelectedRef.current && !selectedConversationId && conversations.length > 0) {
      autoSelectedRef.current = true;
      setSelectedConversationId(conversations[0].id);
    }
  }, [conversations, selectedConversationId]);

  // Single mount per panel: visibility toggles via Tailwind breakpoint classes
  // instead of two parallel JSX trees. A duplicated tree (one hidden by CSS,
  // one shown) still runs its data hooks — duplicate API calls, duplicate SSE
  // subscriptions, and duplicate accessible text that breaks any query keyed
  // by role/text (getByText('Maria Souza') would match both copies at once).
  const hasSelection = !!selectedConversationId;
  return (
    <div className="inbox-workspace">
      <div className="grid grid-cols-1 lg:grid-cols-4 h-[calc(100vh-theme(spacing.16))]">
        {/* ConversationList: full width on mobile when nothing selected; own column on desktop */}
        <div
          className={clsx(
            'lg:col-span-1 lg:border-r lg:border-border-subtle lg:flex lg:flex-col overflow-hidden',
            hasSelection ? 'hidden' : 'flex flex-col'
          )}
        >
          <ConversationListPanel
            conversations={conversations}
            selectedId={selectedConversationId}
            onSelect={setSelectedConversationId}
            segment={segment}
            onSegmentChange={setSegment}
            isLoading={listLoading}
          />
        </div>

        {/* ChatPane column: full width on mobile when selected; own column on desktop
            (placeholder in the same cell when nothing is selected, never a second grid item) */}
        <div className={clsx('lg:col-span-2 lg:flex lg:flex-col', hasSelection ? 'flex flex-col' : 'hidden lg:flex')}>
          {selectedConversationId ? (
            <ChatPane
              conversationId={selectedConversationId}
              onBack={() => setSelectedConversationId(null)}
              onToggleContext={() => setShowContext(!showContext)}
            />
          ) : (
            <div className="flex-1 flex items-center justify-center text-text-secondary">
              Selecione uma conversa
            </div>
          )}
        </div>

        {/* ContextPane: desktop only for now (mobile/tablet sheet is a follow-up slice) */}
        {showContext && selectedConversationId && (
          <div className="hidden lg:flex lg:col-span-1 border-l border-border-subtle overflow-hidden flex-col">
            <ContextPane conversationId={selectedConversationId} />
          </div>
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
