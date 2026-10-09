import React, { useCallback, useEffect, useRef, useState } from 'react';
import clsx from 'clsx';
import { ConversationItem } from '../../types/api';
import { Icon } from '../primitives';
import { WhatsAppName } from '../contacts/WhatsAppName';
import { ChannelBadge } from './ChannelBadge';
import { ChannelOrigin } from './ChannelOrigin';
import type { ChannelLine } from '../../lib/channelLines';
import { INTERNAL_ROLE_LABEL } from '../../lib/contacts';
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
  /** The instance's channel lines. There is no channel filter any more (every channel of the instance is merged in this list); the lines only say where each row came from. */
  channels?: ChannelLine[];
}

const SEGMENTS: { id: InboxSegment; label: string }[] = [
  { id: 'all', label: 'Todas' },
  { id: 'waiting', label: 'Aguardando' },
  { id: 'mine', label: 'Minhas' },
  { id: 'unclassified', label: 'Não classif.' },
  { id: 'internal', label: 'Internas' },
  { id: 'closed', label: 'Encerradas' },
  { id: 'spam', label: 'Spam' },
];

const SCOPES: { id: 'active' | 'closed'; label: string }[] = [
  { id: 'active', label: 'Em andamento' },
  { id: 'closed', label: 'Encerradas' },
];
// segments reached through "Mais…" (the three primary ones are the segmented control)
const MORE_IDS: InboxSegment[] = ['unclassified', 'internal', 'spam'];

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
      <div className="px-4 pt-4 pb-3 flex flex-col gap-3 border-b border-border-subtle">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <h1 className="text-lg font-semibold leading-tight text-text-primary">Conversas</h1>
            <p className="mt-0.5 text-[11px] text-text-tertiary">
              {isLoading ? 'Carregando…' : `${conversations.length}${hasMore ? '+' : ''} ${segment === 'closed' ? 'encerradas' : 'em andamento'}`}
            </p>
          </div>
          <Icon name="conversations" size={20} className="mt-1 flex-shrink-0 text-text-tertiary" />
        </div>
        {/* Scope: what is being attended now, or what was finalized. */}
        <div className="grid grid-cols-2 gap-1" role="tablist" aria-label="Escopo das conversas">
          {SCOPES.map((sc) => {
            const active = sc.id === 'closed' ? segment === 'closed' : segment !== 'closed';
            return (
              <button
                key={sc.id}
                type="button"
                role="tab"
                aria-selected={active}
                onClick={() => onSegmentChange(sc.id === 'closed' ? 'closed' : segment === 'closed' ? 'all' : segment)}
                className={clsx(
                  'h-8 rounded-control px-2 text-[11px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary',
                  active ? 'bg-surface-secondary text-text-primary' : 'text-text-secondary hover:bg-surface-muted'
                )}
              >
                {sc.label}
              </button>
            );
          })}
        </div>
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
        {segment !== 'closed' && (
          <div className="flex items-center gap-1.5">
            <div className="grid flex-1 grid-cols-3 rounded-control bg-surface-muted p-1" role="tablist" aria-label="Filtro de conversas">
              {SEGMENTS.slice(0, 3).map((x) => (
                <button
                  key={x.id}
                  type="button"
                  role="tab"
                  aria-selected={segment === x.id}
                  onClick={() => onSegmentChange(x.id)}
                  className={clsx(
                    'h-7 rounded-control px-1 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary',
                    segment === x.id ? 'bg-surface text-text-primary shadow-sm' : 'text-text-secondary hover:text-text-primary'
                  )}
                >
                  {x.label}
                </button>
              ))}
            </div>
            <label className="flex-shrink-0">
              <span className="sr-only">Mais filtros</span>
              <select
                value={MORE_IDS.includes(segment) ? segment : ''}
                onChange={(e) => e.target.value && onSegmentChange(e.target.value as InboxSegment)}
                className={clsx(
                  'h-9 w-[5.5rem] rounded-control border px-1.5 text-[11px] focus:outline-none focus:ring-2 focus:ring-accent-primary',
                  MORE_IDS.includes(segment) ? 'border-accent-primary bg-accent-primary-soft text-accent-primary' : 'border-transparent bg-surface-muted text-text-secondary'
                )}
              >
                <option value="">Mais…</option>
                {SEGMENTS.slice(3).filter((x) => x.id !== 'closed').map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
        )}
      </div>

      <div ref={scrollRef} onScroll={maybeLoadMore} className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="p-4 text-center text-text-secondary text-sm">Carregando...</div>
        ) : conversations.length === 0 ? (
          <div className="px-4 py-10 text-center">
            <Icon name="conversations" size={24} className="mx-auto mb-2 text-text-tertiary" />
            <p className="text-sm font-medium text-text-primary">Nenhuma conversa</p>
            <p className="mt-1 text-xs text-text-tertiary">
              {search.trim()
                ? 'Nada encontrado para esta busca.'
                : segment === 'closed'
                  ? 'Aqui ficam os atendimentos finalizados.'
                  : segment === 'spam'
                    ? 'Contatos marcados como spam aparecem aqui; abra um e use "Não é spam" para restaurar.'
                    : segment === 'unclassified'
                      ? 'Quando alguém novo escrever, a conversa aparece aqui até você dizer se é cliente ou outro contato.'
                      : segment === 'internal'
                        ? 'Conversas com a sua equipe aparecem aqui quando o número delas é verificado.'
                        : 'Quando um cliente escrever, a conversa aparece aqui.'}
            </p>
            {search.trim() && (
              <button type="button" onClick={() => onSearchChange('')} className="mt-2 text-xs font-medium text-accent-primary hover:underline">
                Limpar busca
              </button>
            )}
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
                origin={conv.channel_connection_id ? lineById.get(conv.channel_connection_id) : undefined}
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
  // a contact an operator declared internal (team on a personal number, partner, supplier): say which, instead of "Outros"
  if (conv.contact_kind === 'internal' && conv.conversation_kind !== 'internal') {
    const role = conv.contact_internal_role
    return <span className={clsx(base, 'bg-accent-primary-soft text-accent-primary')}>{role ? INTERNAL_ROLE_LABEL[role] : 'Interno'}</span>
  }
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
  origin,
  onSelect,
}: {
  conv: ConversationItem;
  selected: boolean;
  now: Date;
  thresholds: WaitThresholds;
  inSpam: boolean;
  line?: ChannelLine;
  /** The line this conversation runs on; says where it came from (logo), whether or not the instance has several lines. */
  origin?: ChannelLine;
  onSelect: (id: string) => void;
}) {
  const name = conv.contact_name || conv.contact_phone;
  // In the Spam inbox nobody is "waiting" for an answer and there is nothing to assign. A conversation with staff is not
  // attendance either (ADR-0018: no waiting metric, nothing to claim).
  const internal = conv.conversation_kind === 'internal';
  const wait = inSpam || internal ? null : waitInfo(conv.waiting_since, now, thresholds);
  const unassigned = !inSpam && !internal && !conv.assigned_to_user_id && conv.status !== 'closed';
  const state = conv.status === 'closed' ? 'Finalizada' : unassigned ? 'Sem atendente' : internal ? 'Interna' : 'Em atendimento';
  return (
    <li>
      <button
        type="button"
        onClick={() => onSelect(conv.id)}
        aria-current={selected ? 'true' : undefined}
        className={clsx(
          'flex w-full items-start gap-3 px-3 py-3 text-left border-b border-border-subtle border-l-2 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent-primary',
          selected ? 'border-l-accent-primary bg-accent-primary-soft' : 'border-l-transparent hover:bg-surface-muted'
        )}
      >
        <span className="relative flex-shrink-0">
          <span className="flex h-9 w-9 items-center justify-center rounded-full bg-surface-muted text-xs font-semibold text-text-secondary">
            {initials(name)}
          </span>
          {unassigned && (
            <span
              title="Sem atendente"
              aria-label="Sem atendente"
              className="absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full bg-status-warning ring-2 ring-surface"
            />
          )}
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex items-baseline justify-between gap-2">
            <span className="min-w-0 truncate text-sm font-semibold text-text-primary">{name}</span>
            <time
              dateTime={conv.last_message_at ?? conv.updated_at}
              className={clsx('flex-shrink-0 text-[11px] tabular-nums', wait ? 'text-accent-primary' : 'text-text-tertiary')}
            >
              {inboxTimeLabel(conv.last_message_at ?? conv.updated_at, now)}
            </time>
          </span>
          <WhatsAppName principal={name} whatsapp={conv.contact_whatsapp_name} />
          {conv.contact_phone && conv.contact_phone !== name && <span className="truncate text-[11px] text-text-tertiary">{conv.contact_phone}</span>}
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
          </span>
          <span className="mt-0.5 flex flex-wrap items-center gap-1.5 text-[10px] text-text-tertiary">
            <ChannelOrigin source={origin?.provider ?? 'whatsapp'} detail={origin?.label} />
            <ChannelBadge line={line} />
            <span className={clsx(unassigned && 'font-medium text-status-warning')}>{state}</span>
            {kindBadge(conv)}
          </span>
        </span>
      </button>
    </li>
  );
}

function initials(name: string): string {
  return (
    name
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((p) => p[0])
      .join('')
      .toUpperCase() || '?'
  );
}
