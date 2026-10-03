import React, { useState, useEffect, useRef } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import clsx from 'clsx';
import { API_BASE } from '../lib/config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../lib/session';
import { useRealtimeEvents } from '../hooks/useRealtimeEvents';
import ConversationListPanel from '../components/inbox/ConversationListPanel';
import ChatPane from '../components/inbox/ChatPane';
import ContextPane from '../components/inbox/ContextPane';
import { InboxSegment } from '../lib/inboxModel';
import type { ConversationItem } from '../types/api';

// PRODUCT.6-O2D2: the frozen deep-link contract is /inbox?conversation_id=
// <uuid> — never /inbox/:id (that path pattern is dead, see InboxPage.tsx/
// ConversationPage.tsx, neither routed in App.tsx). A malformed value is
// treated exactly like an absent one: ignored client-side before ever
// reaching the network, never forwarded as a request.
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

type DeepLinkState = 'idle' | 'resolving' | 'not_found';

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
  const [segment, setSegment] = useState<InboxSegment>('all');
  const [search, setSearch] = useState('');
  const debouncedSearch = useDebounced(search.trim(), 300);
  const [showContext, setShowContext] = useState(true);

  // PRODUCT.6-O2D2 deep link: conversation_id is UNTRUSTED navigation
  // input, never authority. It is resolved through the exact same
  // tenant-scoped, RLS-backed read ChatPane already uses
  // (GET /inbox/conversations/{id}) — reusing its query key so react-query
  // dedupes the request once ChatPane mounts, rather than adding a second
  // implementation or a new backend endpoint.
  const [searchParams] = useSearchParams();
  const rawDeepLinkId = searchParams.get('conversation_id');
  const deepLinkId = rawDeepLinkId && UUID_RE.test(rawDeepLinkId) ? rawDeepLinkId : null;
  const [deepLinkState, setDeepLinkState] = useState<DeepLinkState>('idle');
  const deepLinkRequestIdRef = useRef(0);

  // Conversation list: most recent activity first, paged WITHOUT a cap — the panel asks for the
  // next page as the user scrolls, so nothing is ever unreachable. Search and the "Aguardando" /
  // "Minhas" filters run on the server so they cover every conversation, not just the loaded ones.
  const {
    data: conversationsData,
    isLoading: listLoading,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  } = useInfiniteQuery({
    queryKey: ['inbox-conversations', tenantId, segment, debouncedSearch],
    initialPageParam: '' as string,
    queryFn: async ({ pageParam }) => {
      try {
        const res = await axios.get(`${API_BASE}/tenants/${tenantId}/inbox/conversations`, {
          params: {
            limit: 100,
            cursor: pageParam || undefined,
            q: debouncedSearch || undefined,
            assigned: segment === 'mine' ? 'me' : undefined,
            waiting: segment === 'waiting' ? true : undefined,
          },
          headers: authHeaders(),
        });
        return res.data as { items?: ConversationItem[]; has_more?: boolean; next_cursor?: string };
      } catch (err) {
        if (isUnauthorized(err)) handleUnauthorized();
        throw err;
      }
    },
    getNextPageParam: (last) => (last.has_more && last.next_cursor ? last.next_cursor : undefined),
    enabled: !!tenantId,
  });

  const conversations: ConversationItem[] = conversationsData?.pages.flatMap((page) => page.items ?? []) ?? [];

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
  // A present deep-link id suppresses this entirely (section 5/10 of
  // PRODUCT.6-O2D2): a deep link must never be silently replaced by
  // "whatever happens to be first in the list", including while it is
  // still resolving or if it ultimately fails.
  const autoSelectedRef = useRef(false);
  useEffect(() => {
    if (!autoSelectedRef.current && !selectedConversationId && !deepLinkId && conversations.length > 0) {
      autoSelectedRef.current = true;
      setSelectedConversationId(conversations[0].id);
    }
  }, [conversations, selectedConversationId, deepLinkId]);

  // PRODUCT.6-O2D2: resolve the deep-link id against the real backend
  // BEFORE ever selecting it — never construct a fake Conversation object
  // from the URL alone. tenantID + RLS on the backend remain the only
  // authorization authority; an unknown id and a cross-tenant id are
  // deliberately indistinguishable here, exactly mirroring
  // InboxAPIHandler.GetConversation's own documented 404 contract.
  useEffect(() => {
    if (!deepLinkId || !tenantId) {
      setDeepLinkState('idle');
      return;
    }
    let cancelled = false;
    const requestId = (deepLinkRequestIdRef.current += 1);
    setDeepLinkState('resolving');
    void (async () => {
      try {
        await axios.get(`${API_BASE}/tenants/${tenantId}/inbox/conversations/${deepLinkId}`, {
          headers: authHeaders(),
        });
        if (cancelled || requestId !== deepLinkRequestIdRef.current) return; // superseded by a newer deep link or unmount
        autoSelectedRef.current = true; // a resolved deep link counts as an explicit selection
        setSelectedConversationId(deepLinkId);
        setDeepLinkState('idle');
      } catch (err) {
        if (cancelled || requestId !== deepLinkRequestIdRef.current) return;
        if (isUnauthorized(err)) {
          handleUnauthorized();
          return;
        }
        // 404 covers both "unknown" and "another tenant's conversation" —
        // never distinguished, never leaked, never a constructed fallback.
        setDeepLinkState('not_found');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [deepLinkId, tenantId]);

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
            search={search}
            onSearchChange={setSearch}
            isLoading={listLoading}
            hasMore={!!hasNextPage}
            isFetchingMore={isFetchingNextPage}
            onLoadMore={() => void fetchNextPage()}
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
          ) : deepLinkState === 'resolving' ? (
            <div className="flex-1 flex items-center justify-center text-text-secondary">
              Abrindo conversa…
            </div>
          ) : deepLinkState === 'not_found' ? (
            <div className="flex-1 flex items-center justify-center text-text-secondary">
              Conversa não encontrada ou sem acesso.
            </div>
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

function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = window.setTimeout(() => setDebounced(value), ms);
    return () => window.clearTimeout(id);
  }, [value, ms]);
  return debounced;
}
