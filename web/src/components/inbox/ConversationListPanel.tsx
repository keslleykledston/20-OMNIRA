import React, { useCallback, useEffect, useRef, useState } from 'react';
import clsx from 'clsx';
import { ConversationItem } from '../../types/api';
import { Icon } from '../primitives';
import { WhatsAppName } from '../contacts/WhatsAppName';
import { ChannelBadge } from './ChannelBadge';
import type { ChannelLine } from '../../lib/channelLines';
import { DEFAULT_WAIT_THRESHOLDS, InboxSegment, inboxTimeLabel, previewText, waitInfo, WaitThresholds, WaitTone } from '../../lib/inboxModel';

interface ConversationListPanelProps {
  conversations: ConversationItem[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  segment: InboxSegment;
  onSegmentChange: (segment: InboxSegment) => void;
  search: string;
  onSearchChange: (value: string) => void;
  isLoading: boolean;
  hasMore?: boolean;
  isFetchingMore?: boolean;
  onLoadMore?: () => void;
  waitThresholds?: WaitThresholds;
  /** The tenant's WhatsApp lines; the selector and badges appear only when there is more than one. */
  channels?: ChannelLine[];
  channelFilter?: string;
  onChannelFilterChange?: (id: string) => void;
}

const SEGMENTS: { id: InboxSegment; label: string }[] = [
  { id: 'all', label: 'Todas' },
  { id: 'waiting', label: 'Aguardando' },
  { id: 'mine', label: 'Minhas' },
  { id: 'unclassified', label: 'Não classif.' },
  { id: 'internal', label: 'Internas' },
  { id: 'spam', label: 'Spam' },
];

const WAIT_STYLE: Record<WaitTone, string> = {
  muted: 'bg-surface-muted text-text-secondary',
  warning: 'bg-status-warning-soft text-status-warning',
  danger: 'bg-status-danger-soft text-status-danger',
};

// Labels ("14:32", "5 min") move with the clock, so re-render once a minute.
function useMinuteClock(): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = window.setInterval(() => setNow(new Date()), 60_000);
    return () => window.clearInterval(id);
  }, []);
  return now;
}

export default function ConversationListPanel({
  conversations,
  selectedId,
  onSelect,
  segment,
  onSegmentChange,
  search,
  onSearchChange,
  isLoading,
  hasMore = false,
  isFetchingMore = false,
  onLoadMore,
  waitThresholds = DEFAULT_WAIT_THRESHOLDS,
  channels = [],
  channelFilter = '',
  onChannelFilterChange,
}: ConversationListPanelProps) {
  const multiChannel = channels.length > 1;
  const lineById = new Map(channels.map((c) => [c.id, c] as const));
  const now = useMinuteClock();
  const scrollRef = useRef<HTMLDivElement>(null);

  // Load the next page when the user nears the bottom — and also when the loaded rows do not
  // even fill the panel (tall screen), so there is never a "stuck" short list with more behind it.
  const maybeLoadMore = useCallback(() => {
    const el = scrollRef.current;
    if (!el || !hasMore || isFetchingMore || !onLoadMore) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 240) onLoadMore();
  }, [hasMore, isFetchingMore, onLoadMore]);

  useEffect(() => {
    maybeLoadMore();
  }, [conversations.length, maybeLoadMore]);

  return (
    <div className="flex flex-col h-full bg-surface">
      <div className="px-3 pt-3 pb-2 flex flex-col gap-2 border-b border-border-subtle">
        <div className="flex gap-1.5" role="tablist" aria-label="Filtro de conversas">
          {SEGMENTS.map((s) => (
            <button
              key={s.id}
              type="button"
              role="tab"
              aria-selected={segment === s.id}
              onClick={() => onSegmentChange(s.id)}
              className={clsx(
                'px-2.5 py-1 text-xs font-medium rounded-pill transition-colors',
                segment === s.id
                  ? 'bg-accent-primary text-white'
                  : 'bg-surface-muted text-text-secondary hover:bg-surface-tertiary'
              )}
            >
              {s.label}
            </button>
          ))}
        </div>
        {multiChannel && onChannelFilterChange && (
          <label className="block">
            <span className="sr-only">Filtrar por canal</span>
            <select
              value={channelFilter}
              onChange={(e) => onChannelFilterChange(e.target.value)}
              className="w-full rounded-control border border-transparent bg-surface-muted px-2.5 py-1.5 text-sm text-text-primary focus:outline-none focus:ring-2 focus:ring-accent-primary"
            >
              <option value="">Todos os canais</option>
              {channels.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.label}
                </option>
              ))}
            </select>
          </label>
        )}
        <label className="relative block">
          <span className="sr-only">Buscar contato</span>
          <Icon name="search" size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-text-tertiary" />
          <input
            type="search"
            value={search}
            onChange={(e) => onSearchChange(e.target.value)}
            placeholder="Buscar contato..."
            className={clsx(
              'w-full pl-8 pr-3 py-1.5 text-sm rounded-control',
              'bg-surface-muted text-text-primary placeholder:text-text-tertiary',
              'focus:outline-none focus:ring-2 focus:ring-accent-primary border border-transparent'
            )}
          />
        </label>
      </div>

      <div ref={scrollRef} onScroll={maybeLoadMore} className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="p-4 text-center text-text-secondary text-sm">Carregando...</div>
        ) : conversations.length === 0 ? (
          <div className="p-4 text-center text-text-tertiary text-sm">
            {search.trim()
              ? 'Nenhuma conversa encontrada'
              : segment === 'all'
                ? 'Nenhuma conversa'
                : segment === 'spam'
                  ? 'Nenhum spam. Aqui ficam os contatos marcados como spam: abra um e use "Não é spam" para restaurar.'
                  : segment === 'unclassified'
                    ? 'Nenhuma conversa sem classificação. Quando alguém novo escrever, ela aparece aqui até você dizer se é cliente ou outro contato.'
                    : segment === 'internal'
                      ? 'Nenhuma conversa interna. Conversas com a sua equipe aparecem aqui quando o número delas é verificado.'
                      : 'Nada por aqui'}
          </div>
        ) : (
          <ul>
            {conversations.map((conv) => (
              <ConversationRow
                key={conv.id}
                conv={conv}
                selected={selectedId === conv.id}
                now={now}
                thresholds={waitThresholds}
                inSpam={segment === 'spam'}
                line={multiChannel && conv.channel_connection_id ? lineById.get(conv.channel_connection_id) : undefined}
                onSelect={onSelect}
              />
            ))}
          </ul>
        )}
        {isFetchingMore && <div className="py-3 text-center text-xs text-text-tertiary">Carregando mais...</div>}
      </div>
    </div>
  );
}

// What the conversation is, when it is not plain customer service (ADR-0018). A customer conversation shows nothing: it is
// the normal case. "Não classificado" tells the attendant to say who this is before treating it as a customer.
function kindBadge(conv: ConversationItem) {
  const base = 'flex-shrink-0 rounded-pill px-1.5 text-[10px] font-medium leading-4'
  switch (conv.conversation_kind) {
    case 'internal':
      return <span className={clsx(base, 'bg-surface-muted text-text-secondary')}>Interna</span>
    case 'unclassified':
      return <span className={clsx(base, 'bg-status-warning-soft text-status-warning')}>Não classificado</span>
    case 'external_other':
      return <span className={clsx(base, 'bg-surface-muted text-text-secondary')}>Outros</span>
    default:
      return null
  }
}

function ConversationRow({
  conv,
  selected,
  now,
  thresholds,
  inSpam,
  line,
  onSelect,
}: {
  conv: ConversationItem;
  selected: boolean;
  now: Date;
  thresholds: WaitThresholds;
  inSpam: boolean;
  line?: ChannelLine;
  onSelect: (id: string) => void;
}) {
  const name = conv.contact_name || conv.contact_phone;
  // In the Spam inbox nobody is "waiting" for an answer and there is nothing to assign. A conversation with staff is not
  // attendance either (ADR-0018: no waiting metric, nothing to claim).
  const internal = conv.conversation_kind === 'internal';
  const wait = inSpam || internal ? null : waitInfo(conv.waiting_since, now, thresholds);
  const unassigned = !inSpam && !internal && !conv.assigned_to_user_id && conv.status !== 'closed';
  return (
    <li>
      <button
        type="button"
        onClick={() => onSelect(conv.id)}
        aria-current={selected ? 'true' : undefined}
        className={clsx(
          'flex w-full items-center gap-2.5 px-3 py-2 text-left border-b border-border-subtle transition-colors',
          selected ? 'bg-accent-primary-soft' : 'hover:bg-surface-muted'
        )}
      >
        <span className="relative flex-shrink-0">
          <span className="flex h-9 w-9 items-center justify-center rounded-full bg-accent-primary text-sm font-semibold text-white">
            {name?.[0]?.toUpperCase() || '?'}
          </span>
          {unassigned && (
            <span
              title="Sem atendente"
              aria-label="Sem atendente"
              className="absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full bg-status-warning ring-2 ring-surface"
            />
          )}
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-baseline justify-between gap-2">
            <span className="min-w-0">
              <span className="block truncate text-sm font-semibold text-text-primary">{name}</span>
              <WhatsAppName principal={name} whatsapp={conv.contact_whatsapp_name} />
            </span>
            <time
              dateTime={conv.last_message_at ?? conv.updated_at}
              className={clsx('flex-shrink-0 text-[11px] tabular-nums', wait ? 'text-accent-primary' : 'text-text-tertiary')}
            >
              {inboxTimeLabel(conv.last_message_at ?? conv.updated_at, now)}
            </time>
          </span>
          <span className="flex items-center justify-between gap-2">
            <span className="truncate text-xs text-text-secondary">{previewText(conv)}</span>
            {wait && (
              <span
                title="Cliente aguardando resposta"
                className={clsx(
                  'inline-flex flex-shrink-0 items-center gap-0.5 rounded-pill px-1.5 text-[10px] font-medium leading-4 tabular-nums',
                  WAIT_STYLE[wait.tone]
                )}
              >
                <Icon name="clock" size={10} />
                {wait.label}
              </span>
            )}
            <ChannelBadge line={line} />
            {kindBadge(conv)}
            {conv.status === 'closed' && (
              <span className="flex-shrink-0 rounded-pill bg-status-muted px-1.5 text-[10px] font-medium leading-4 text-text-secondary">
                Fechada
              </span>
            )}
          </span>
        </span>
      </button>
    </li>
  );
}
