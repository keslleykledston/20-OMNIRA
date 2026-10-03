import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import clsx from 'clsx';
import { MessageItem, ConversationItem } from '../../types/api';
import { API_BASE } from '../../lib/config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session';
import { useRealtimeEvents } from '../../hooks/useRealtimeEvents';
import MessageBubble from './MessageBubble';
import MessageComposer from './MessageComposer';
import { Icon } from '../primitives';
import { dayLabel, sortChronological } from '../../lib/inboxModel';

interface ChatPaneProps {
  conversationId: string;
  onBack?: () => void;
  onToggleContext?: () => void;
}

export default function ChatPane({ conversationId, onBack, onToggleContext }: ChatPaneProps) {
  const tenantId = getTenantId();
  const queryClient = useQueryClient();
  const [conversation, setConversation] = useState<ConversationItem | null>(null);
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  // One Idempotency-Key per distinct text: a retry after a network failure or a
  // timeout re-sends the same key, so the backend (required header, 8-128 chars
  // of [A-Za-z0-9._:-] per contracts/openapi/omnira-v1.yaml) never queues a
  // duplicate for the same attempt.
  const pendingSend = useRef<{ text: string; key: string } | null>(null);

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

  // Fetch messages: the API pages newest-first; older pages load when the user scrolls to the top.
  const {
    data: messagesData,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  } = useInfiniteQuery({
    queryKey: ['inbox-messages', tenantId, conversationId],
    initialPageParam: '' as string,
    queryFn: async ({ pageParam }) => {
      try {
        const res = await axios.get(
          `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/messages`,
          { params: { limit: 100, cursor: pageParam || undefined }, headers: authHeaders() }
        );
        return res.data as { items?: MessageItem[]; has_more?: boolean; next_cursor?: string };
      } catch (err) {
        if (isUnauthorized(err)) handleUnauthorized();
        throw err;
      }
    },
    getNextPageParam: (last) => (last.has_more && last.next_cursor ? last.next_cursor : undefined),
    enabled: !!tenantId && !!conversationId,
  });

  // Oldest first, newest at the bottom next to the composer. Pages can overlap after a refetch
  // (a new message shifts the boundary), so de-duplicate by id.
  const messages = useMemo(
    () => sortChronological(messagesData?.pages.flatMap((page) => page.items ?? []) ?? []),
    [messagesData]
  );

  useEffect(() => {
    if (conversationData) setConversation(conversationData);
  }, [conversationData]);

  // Realtime events
  useRealtimeEvents({
    tenantId,
    conversationId,
    onEvent: () => {
      void queryClient.invalidateQueries({ queryKey: ['inbox-messages', tenantId, conversationId] });
      void queryClient.invalidateQueries({ queryKey: ['inbox-conversation-detail', tenantId, conversationId] });
    },
  });

  // Scrolling. The thread opens on the newest message and follows new ones (received or sent)
  // while the user is at the bottom; if they scrolled up to read, it stays put and offers a button
  // back. Loading an older page keeps the view where it was instead of jumping.
  const scrollRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const stickRef = useRef(true);
  const prependRef = useRef<{ height: number; top: number } | null>(null);
  const lastIdRef = useRef<string | null>(null);
  const openedRef = useRef<string | null>(null);
  const [atBottom, setAtBottom] = useState(true);

  const scrollToBottom = () => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  };

  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el || messages.length === 0) return;
    const lastMsg = messages[messages.length - 1];
    if (openedRef.current !== conversationId) {
      openedRef.current = conversationId;
      lastIdRef.current = lastMsg.id;
      stickRef.current = true;
      scrollToBottom();
      return;
    }
    if (prependRef.current) {
      el.scrollTop = el.scrollHeight - prependRef.current.height + prependRef.current.top;
      prependRef.current = null;
      return;
    }
    if (lastMsg.id !== lastIdRef.current) {
      lastIdRef.current = lastMsg.id;
      if (stickRef.current || lastMsg.direction === 'outbound') {
        stickRef.current = true;
        scrollToBottom();
      }
    }
  }, [messages, conversationId]);

  // Images/audio finish loading after the first paint and grow the thread: keep following the end.
  useEffect(() => {
    const content = contentRef.current;
    if (!content || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => {
      if (stickRef.current) scrollToBottom();
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, [conversationId]);

  const requestOlder = () => {
    const el = scrollRef.current;
    if (!el || !hasNextPage || isFetchingNextPage) return;
    prependRef.current = { height: el.scrollHeight, top: el.scrollTop };
    void fetchNextPage();
  };

  const handleScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 160;
    stickRef.current = nearBottom;
    setAtBottom(nearBottom);
    if (el.scrollTop < 80) requestOlder();
  };

  const handleSendMessage = async (text: string): Promise<boolean> => {
    if (!text.trim()) return false;

    setSending(true);
    setSendError(null);

    // Reuse the key on a retry of the exact same text; a different text is a
    // new attempt and gets its own key.
    if (pendingSend.current?.text !== text) {
      pendingSend.current = { text, key: crypto.randomUUID() };
    }
    const idempotencyKey = pendingSend.current.key;

    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/messages`,
        { text },
        { headers: { ...authHeaders(), 'Idempotency-Key': idempotencyKey } }
      );
      pendingSend.current = null;
      // Reset and refetch
      void queryClient.invalidateQueries({ queryKey: ['inbox-messages', tenantId, conversationId] });
      return true;
    } catch (err: any) {
      if (isUnauthorized(err)) { handleUnauthorized(); return false; }
      setSendError(describeSendError(err));
      return false;
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="flex flex-col h-full bg-surface">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2.5 border-b border-border-subtle">
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

      {/* Timeline: oldest at the top, newest at the bottom next to the composer. A short thread
          sits at the bottom too (justify-end), like WhatsApp. */}
      <div className="relative flex-1 min-h-0">
        <div ref={scrollRef} onScroll={handleScroll} className="h-full overflow-y-auto">
          <div ref={contentRef} className="flex min-h-full flex-col justify-end gap-0.5 px-3 py-2">
            {hasNextPage && (
              <button
                type="button"
                onClick={requestOlder}
                disabled={isFetchingNextPage}
                className="mx-auto mb-1 rounded-pill bg-surface-muted px-3 py-1 text-xs text-text-secondary hover:bg-surface-tertiary disabled:opacity-60"
              >
                {isFetchingNextPage ? 'Carregando mensagens anteriores...' : 'Carregar mensagens anteriores'}
              </button>
            )}
            {messages.length === 0 ? (
              <div className="flex items-center justify-center py-10 text-text-tertiary text-sm">Nenhuma mensagem</div>
            ) : (
              messages.map((msg, idx) => {
                const prev = idx > 0 ? messages[idx - 1] : null;
                const label = dayLabel(msg.created_at);
                const showDay = !prev || dayLabel(prev.created_at) !== label;
                const turn = prev && prev.direction !== msg.direction && !showDay;
                return (
                  <div key={msg.id} className={turn ? 'mt-1.5' : undefined}>
                    {showDay && (
                      <div className="my-2 flex justify-center">
                        <span className="rounded-pill bg-surface-muted px-3 py-0.5 text-[11px] text-text-secondary">{label}</span>
                      </div>
                    )}
                    <MessageBubble message={msg} />
                  </div>
                );
              })
            )}
          </div>
        </div>
        {!atBottom && (
          <button
            type="button"
            onClick={() => {
              stickRef.current = true;
              scrollToBottom();
              setAtBottom(true);
            }}
            aria-label="Ir para a última mensagem"
            className="absolute bottom-3 right-4 flex h-9 w-9 items-center justify-center rounded-full border border-border-subtle bg-surface text-text-secondary shadow-sm hover:bg-surface-muted"
          >
            <Icon name="arrow-left" size={16} className="-rotate-90" />
          </button>
        )}
      </div>

      {/* Composer */}
      <div className="px-3 py-2 border-t border-border-subtle">
        {sendError && (
          <div role="alert" className="mb-3 p-2 bg-status-danger-soft text-status-danger text-xs rounded-control">
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

// POST .../messages returns a plain-text body on error (internal/messages/adapters/http.go
// fail()), not JSON — err.response.data is the string itself.
function describeSendError(err: any): string {
  const status = err?.response?.status;
  const body = typeof err?.response?.data === 'string' ? err.response.data : '';
  if (status === 409) {
    return body.includes('assigned')
      ? 'Assuma esta conversa antes de responder.'
      : 'A conversa mudou, tente novamente.';
  }
  if (status === 403) return 'Você não pode responder — esta conversa não está atribuída a você.';
  if (status === 422) return body || 'Mensagem inválida.';
  return body || 'Erro ao enviar mensagem.';
}
