import { WhatsAppName } from '../contacts/WhatsAppName';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import clsx from 'clsx';
import { MessageItem, ConversationItem } from '../../types/api';
import { API_BASE } from '../../lib/config';
import { authHeaders, currentUserId, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session';
import { useRealtimeEvents } from '../../hooks/useRealtimeEvents';
import { useThreadScroll } from '../../hooks/useThreadScroll';
import MessageBubble from './MessageBubble';
import MessageComposer from './MessageComposer';
import { uploadAttachment, removeAttachment, describeAttachmentError } from '../../lib/attachments';
import { Icon } from '../primitives';
import { dayLabel, sortChronological } from '../../lib/inboxModel';
import { useChannelLines, useConversationChannel } from '../../lib/channelLines';
import { ChannelBadge } from './ChannelBadge';
import { TemplateSendDialog } from './TemplateSendDialog';
import FinalizeDialog from './FinalizeDialog';
import { emitInboxNotice, useInboxNotice } from '../../lib/inboxNotice';
import { useAccess } from '../../lib/useAccess';
import TenantContextBar from './TenantContextBar';

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
  const pendingSend = useRef<{ text: string; attachmentId?: string; key: string } | null>(null);

  const lines = useChannelLines();
  const channel = useConversationChannel(conversationId);
  const line = (lines.data ?? []).find((l) => l.id === conversation?.channel_connection_id);
  const cs = channel.data;
  const windowClosed = !!cs && cs.window_required && !cs.window_open;
  const channelDown = !!cs && !cs.can_send_text;

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

  // Scrolling (open on the newest, follow what arrives, keep position when older pages load): see
  // useThreadScroll. A message we sent always scrolls into view.
  const followSent = useCallback((m: MessageItem) => m.direction === 'outbound', []);
  const { scrollRef, contentRef, atBottom, onScroll, requestOlder, jumpToBottom } = useThreadScroll({
    threadKey: conversationId,
    messages,
    alwaysFollow: followSent,
    hasMore: !!hasNextPage,
    isFetchingMore: isFetchingNextPage,
    fetchMore: fetchNextPage,
  });

  const [claiming, setClaiming] = useState(false);
  const [showTemplates, setShowTemplates] = useState(false);
  const [showFinalize, setShowFinalize] = useState(false);
  const [notice, clearNotice] = useInboxNotice(conversationId);
  const { can } = useAccess();
  const metaLine = !!cs && cs.window_required;
  const claim = async () => {
    setClaiming(true);
    setSendError(null);
    try {
      await axios.post(`${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/assign`, {}, { headers: authHeaders() });
      await queryClient.invalidateQueries({ queryKey: ['inbox-conversation-detail', tenantId, conversationId] });
      await queryClient.invalidateQueries({ queryKey: ['inbox-context', tenantId, conversationId] });
      void queryClient.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] });
      emitInboxNotice(conversationId, 'Atendimento assumido por você.');
    } catch (err: any) {
      if (isUnauthorized(err)) handleUnauthorized();
      else setSendError(err?.response?.status === 409 ? 'Este atendimento acabou de ser assumido por outro operador.' : 'Não foi possível assumir o atendimento.');
    } finally {
      setClaiming(false);
    }
  };
  const unassigned = !!conversation && conversation.status !== 'closed' && !conversation.assigned_to_user_id && conversation.conversation_kind !== 'internal';
  const mine = !!conversation?.assigned_to_user_id && conversation.assigned_to_user_id === currentUserId();
  const ownerName = conversation?.assigned_to_name?.trim() || '';
  const owner = !conversation?.assigned_to_user_id
    ? 'Sem responsável'
    : mine
      ? ownerName ? `${ownerName} (você)` : 'Você'
      : ownerName || 'Outro atendente';

  const handleSendMessage = async (text: string, attachmentId?: string): Promise<boolean> => {
    // a file may go without a caption; plain text still needs text
    if (!text.trim() && !attachmentId) return false;

    setSending(true);
    setSendError(null);

    // Reuse the key on a retry of the exact same text; a different text is a
    // new attempt and gets its own key.
    if (pendingSend.current?.text !== text || pendingSend.current?.attachmentId !== attachmentId) {
      pendingSend.current = { text, attachmentId, key: crypto.randomUUID() };
    }
    const idempotencyKey = pendingSend.current.key;

    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/messages`,
        attachmentId ? { text, attachment_id: attachmentId } : { text },
        { headers: { ...authHeaders(), 'Idempotency-Key': idempotencyKey } }
      );
      pendingSend.current = null;
      // Reset and refetch
      void queryClient.invalidateQueries({ queryKey: ['inbox-messages', tenantId, conversationId] });
      return true;
    } catch (err: any) {
      if (isUnauthorized(err)) { handleUnauthorized(); return false; }
      setSendError(attachmentId ? describeAttachmentError(err) : describeSendError(err));
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
              className="md:hidden p-1 hover:bg-surface-muted rounded-control"
              title="Voltar"
            >
              <Icon name="arrow-left" />
            </button>
          )}
          <span className="hidden h-9 w-9 flex-shrink-0 items-center justify-center rounded-full bg-surface-muted text-xs font-semibold text-text-secondary sm:flex">
            {(conversation?.contact_name || '?').split(/\s+/).filter(Boolean).slice(0, 2).map((p) => p[0]).join('').toUpperCase() || '?'}
          </span>
          <div className="min-w-0">
            <h3 className="font-semibold text-text-primary truncate">
              {conversation?.contact_name}
            </h3>
            <WhatsAppName principal={conversation?.contact_name} whatsapp={conversation?.contact_whatsapp_name} />
            <p className="flex items-center gap-1.5 truncate text-[11px] text-text-secondary">
              <span className="truncate">
                {line ? (line.provider_kind === 'official' ? 'WhatsApp oficial' : 'WhatsApp') : 'Canal não informado'} · {conversation?.contact_phone}
              </span>
              {(lines.data ?? []).length > 1 && <ChannelBadge line={line} />}
            </p>
          </div>
        </div>

        {/* Actions */}
        <div className="flex gap-2">
          {onToggleContext && (
            <button
              onClick={onToggleContext}
              className="p-2 hover:bg-surface-muted rounded-control"
              title="Detalhes do atendimento"
              aria-label="Abrir detalhes do atendimento"
            >
              <Icon name="info" />
            </button>
          )}
        </div>
      </div>

      <TenantContextBar />
      {conversation && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border-subtle bg-surface px-4 py-1.5 text-[11px]">
          <span
            className={clsx(
              'rounded-pill px-2 py-0.5 font-medium',
              conversation.status === 'closed' ? 'bg-status-muted text-text-secondary' : 'bg-status-success-soft text-status-success',
            )}
          >
            {conversation.status === 'closed' ? 'Finalizado' : 'Em atendimento'}
          </span>
          <span className="inline-flex items-center gap-1 text-text-secondary">
            <Icon name="contacts" size={12} />
            {owner}
          </span>
          {unassigned && (
            <button type="button" onClick={() => void claim()} disabled={claiming} className="ml-auto font-medium text-accent-primary hover:underline disabled:opacity-60">
              {claiming ? 'Assumindo…' : 'Assumir atendimento'}
            </button>
          )}
          {mine && conversation.status !== 'closed' && conversation.conversation_kind !== 'internal' && (
            <button type="button" onClick={() => setShowFinalize(true)} className="ml-auto rounded-control px-1.5 py-0.5 font-medium text-text-secondary hover:bg-surface-muted hover:text-text-primary">
              Finalizar
            </button>
          )}
        </div>
      )}
      {notice && (
        <div role="status" className="flex items-center justify-between gap-2 border-b border-border-subtle bg-status-info-soft px-4 py-2 text-xs text-status-info">
          <span>{notice}</span>
          <button type="button" onClick={clearNotice} aria-label="Fechar aviso" className="text-status-info/70 hover:text-status-info">×</button>
        </div>
      )}

      {/* Timeline: oldest at the top, newest at the bottom next to the composer. A short thread
          sits at the bottom too (justify-end), like WhatsApp. */}
      <div className="relative flex-1 min-h-0">
        <div ref={scrollRef} onScroll={onScroll} className="absolute inset-0 overflow-y-auto">
          <div ref={contentRef} className="flex min-h-full flex-col justify-end gap-px px-3 py-2">
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
            {!hasNextPage && conversation?.created_at && (
              <p className="mb-1 flex items-center justify-center gap-1.5 text-[10px] text-text-tertiary">
                <Icon name="clock" size={12} />
                Início do atendimento · {new Date(conversation.created_at).toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })}
              </p>
            )}
            {messages.length === 0 ? (
              <div className="flex items-center justify-center py-10 text-text-tertiary text-sm">Nenhuma mensagem neste atendimento.</div>
            ) : (
              messages.map((msg, idx) => {
                const prev = idx > 0 ? messages[idx - 1] : null;
                const label = dayLabel(msg.created_at);
                const showDay = !prev || dayLabel(prev.created_at) !== label;
                const turn = prev && prev.direction !== msg.direction && !showDay;
                // the speaker is named once, on the first bubble of a run from the same side
                const firstOfRun = !prev || showDay || prev.direction !== msg.direction;
                return (
                  <div key={msg.id} className={turn ? 'mt-1' : undefined}>
                    {showDay && (
                      <div className="my-2 flex items-center gap-3 text-[10px] text-text-tertiary">
                        <span className="h-px flex-1 bg-border-subtle" />
                        <span>{label}</span>
                        <span className="h-px flex-1 bg-border-subtle" />
                      </div>
                    )}
                    <MessageBubble message={msg} sender={firstOfRun ? (msg.direction === 'outbound' ? 'Equipe' : conversation?.contact_name) : undefined} />
                  </div>
                );
              })
            )}
            {conversation?.status === 'closed' && (
              <p className="mt-2 rounded-control bg-surface-muted px-3 py-1.5 text-center text-[11px] text-text-secondary">
                Atendimento encerrado · histórico disponível para consulta
              </p>
            )}
          </div>
        </div>
        {!atBottom && (
          <button
            type="button"
            onClick={jumpToBottom}
            aria-label="Ir para a última mensagem"
            className="absolute bottom-3 right-4 flex h-9 w-9 items-center justify-center rounded-full border border-border-subtle bg-surface text-text-secondary shadow-sm hover:bg-surface-muted"
          >
            <Icon name="arrow-left" size={16} className="-rotate-90" />
          </button>
        )}
      </div>

      {/* Composer. Replying to a spam contact is switched off (ADR-0014): restore it first. */}
      {conversation?.status === 'closed' ? (
        <div role="status" className="border-t border-border-subtle bg-surface-muted px-4 py-3 text-xs text-text-secondary">
          Este atendimento está encerrado. O envio está indisponível. Se o contato escrever de novo, abre-se um atendimento novo.
        </div>
      ) : unassigned && conversation?.contact_kind !== 'spam' ? (
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle bg-surface-muted px-4 py-3 text-xs">
          <span role="status" className="text-text-secondary">Assuma o atendimento para responder.</span>
          <button type="button" onClick={() => void claim()} disabled={claiming} className="rounded-control bg-accent-primary px-3 py-1.5 font-medium text-white hover:bg-accent-primary-hover disabled:opacity-60">
            {claiming ? 'Assumindo…' : 'Assumir'}
          </button>
        </div>
      ) : !!conversation?.assigned_to_user_id && !mine && !can('conversation.manage') && conversation.contact_kind !== 'spam' ? (
        <div role="status" className="border-t border-border-subtle bg-surface-muted px-4 py-3 text-xs text-text-secondary">
          Atendimento com {ownerName || 'outro atendente'}. Resposta indisponível.
        </div>
      ) : conversation?.contact_kind === 'spam' ? (
        <div className="border-t border-border-subtle bg-surface-muted px-4 py-3 text-xs text-text-secondary">
          Contato marcado como spam: as respostas ficam desativadas. Use <strong>Não é spam</strong> no painel ao lado para restaurar.
        </div>
      ) : (
      <div className="px-3 py-2 border-t border-border-subtle">
        {sendError && (
          <div role="alert" className="mb-3 p-2 bg-status-danger-soft text-status-danger text-xs rounded-control">
            {sendError}
          </div>
        )}
        {channelDown && (
          <div role="status" className="mb-2 rounded-control bg-status-warning-soft p-2 text-xs text-status-warning">
            O canal desta conversa está desconectado. Reconecte-o em Canais ou fale com a pessoa por outro canal pelo painel ao lado.
          </div>
        )}
        {windowClosed && (
          <div role="status" className="mb-2 rounded-control bg-status-warning-soft p-2 text-xs text-status-warning">
            Janela de 24 h fechada: neste número a Meta só aceita mensagem de template até o cliente escrever de novo.{' '}
            <button type="button" onClick={() => setShowTemplates(true)} className="font-semibold underline">
              Enviar template
            </button>
          </div>
        )}
        {cs?.window_required && cs.window_open && cs.window_expires_at && (
          <div className="mb-2 flex items-center justify-between gap-2 text-[11px] text-text-tertiary">
            <span>Resposta livre permitida até {new Date(cs.window_expires_at).toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })}.</span>
            <button type="button" onClick={() => setShowTemplates(true)} className="font-medium text-accent-primary hover:underline">Enviar template</button>
          </div>
        )}
        <MessageComposer
          draftKey={`${tenantId}:${conversationId}`}
          onSend={handleSendMessage}
          onAttach={cs?.can_send_media ? (file) => uploadAttachment(tenantId, conversationId, file) : undefined}
          onRemoveAttachment={(id) => void removeAttachment(tenantId, conversationId, id)}
          describeError={describeAttachmentError}
          label={`Responder ao contato${line ? ` · ${line.label}` : ''}`}
          labelRight={conversation?.contact_name}
          disabled={sending || windowClosed || channelDown}
          placeholder="Escreva uma resposta..."
        />
      </div>
      )}
      <FinalizeDialog
        open={showFinalize}
        conversationId={conversationId}
        contactName={conversation?.contact_name}
        onClose={() => setShowFinalize(false)}
        onFinalized={() => emitInboxNotice(conversationId, 'Atendimento encerrado. O histórico foi preservado.')}
      />
      {metaLine && (
        <TemplateSendDialog
          open={showTemplates}
          conversationId={conversationId}
          connectionId={conversation?.channel_connection_id}
          contactName={conversation?.contact_name}
          onClose={() => setShowTemplates(false)}
        />
      )}
    </div>
  );
}

// POST .../messages returns a plain-text body on error (internal/messages/adapters/http.go
// fail()), not JSON — err.response.data is the string itself.
function describeSendError(err: any): string {
  const status = err?.response?.status;
  const body = typeof err?.response?.data === 'string' ? err.response.data : '';
  if (status === 409) {
    if (body.includes('window')) return 'Janela de 24 h da Meta fechada: só mensagem de template até o cliente escrever de novo.';
    return body.includes('assigned')
      ? 'Assuma esta conversa antes de responder.'
      : 'A conversa mudou, tente novamente.';
  }
  if (status === 403) return 'Você não pode responder — esta conversa não está atribuída a você.';
  if (status === 422) return body || 'Mensagem inválida.';
  return body || 'Erro ao enviar mensagem.';
}
