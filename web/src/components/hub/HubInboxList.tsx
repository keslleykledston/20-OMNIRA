import clsx from 'clsx';
import { ChannelOrigin, channelLabel } from '../inbox/ChannelOrigin';
import type { HubInboxItem } from '../../lib/hub';
import { TenantBadge } from '../primitives/TenantBadge';

function when(iso?: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay ? d.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' }) : d.toLocaleDateString('pt-BR', { day: '2-digit', month: '2-digit' });
}

const PRIORITY: Record<string, string> = { high: 'Alta', urgent: 'Urgente' };

interface Props {
  items: HubInboxItem[];
  selectedId: string;
  onSelect: (id: string) => void;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}

// One row per conversation, always showing WHICH company it belongs to (code + full name, never colour alone).
export default function HubInboxList({ items, selectedId, onSelect, hasMore, loadingMore, onLoadMore }: Props) {
  return (
    <div className="flex h-full flex-col overflow-y-auto">
      <ul aria-label="Conversas do Hub" className="divide-y divide-border-subtle">
        {items.map((it) => {
          const selected = it.id === selectedId;
          return (
            <li key={it.id}>
              <button
                type="button"
                onClick={() => onSelect(it.id)}
                aria-pressed={selected}
                className={clsx(
                  'flex w-full flex-col gap-1 px-4 py-3 text-left transition-colors focus-visible:ring-2 focus-visible:ring-accent-primary',
                  selected ? 'bg-accent-primary-soft' : 'hover:bg-surface-muted',
                )}
              >
                <span className="flex items-baseline justify-between gap-2">
                  <span className="truncate text-sm font-semibold text-text-primary">{it.customer_name || 'Sem nome'}</span>
                  <time className="flex-shrink-0 text-[11px] text-text-tertiary">{when(it.last_activity_at)}</time>
                </span>
                <span className="flex items-center gap-2 text-[11px]">
                  <TenantBadge name={it.tenant_name || 'Instância não identificada'} />
                </span>
                <span className="flex items-center gap-2 text-[11px] text-text-secondary">
                  <ChannelOrigin source={it.channel} />
                  <span>{channelLabel(it.channel)}</span>
                  {it.status === 'closed' && <span className="rounded-pill bg-status-muted px-2 py-0.5">Finalizado</span>}
                  {PRIORITY[it.priority] && <span className="rounded-pill bg-status-warning-soft px-2 py-0.5 text-status-warning-strong">{PRIORITY[it.priority]}</span>}
                  {it.unread_count > 0 && (
                    <span className="ml-auto rounded-pill bg-accent-primary px-2 py-0.5 font-semibold text-white" aria-label={`${it.unread_count} sem resposta`}>
                      {it.unread_count}
                    </span>
                  )}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      {hasMore && (
        <div className="p-3 text-center">
          <button type="button" onClick={onLoadMore} disabled={loadingMore} className="rounded-control px-3 py-1.5 text-sm font-medium text-accent-primary hover:bg-surface-muted disabled:opacity-60">
            {loadingMore ? 'Carregando…' : 'Carregar mais'}
          </button>
        </div>
      )}
    </div>
  );
}
