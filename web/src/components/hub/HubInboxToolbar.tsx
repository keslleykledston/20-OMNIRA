import clsx from 'clsx';
import { Icon } from '../primitives';
import type { HubSegment, HubSegmentCounts } from '../../lib/hub';

// What the person is looking at in the Hub inbox: the attendances in progress (all / the queue of pending ones / mine) or the history
// (finalized). The same two-level layout as the tenant inbox's list (scope, then filter), so the Hub does not invent a second way to read the queue.
export type HubView = 'all' | 'waiting' | 'mine' | 'closed';

// How a view is asked of the server.
export const segmentOf = (view: HubView): HubSegment => (view === 'all' ? 'open' : view);

const SCOPES: { id: 'active' | 'closed'; label: string }[] = [
  { id: 'active', label: 'Em andamento' },
  { id: 'closed', label: 'Encerradas' },
];
const FILTERS: { id: Exclude<HubView, 'closed'>; label: string; count: keyof HubSegmentCounts }[] = [
  { id: 'all', label: 'Todas', count: 'open' },
  { id: 'waiting', label: 'Pendentes', count: 'waiting' },
  { id: 'mine', label: 'Minhas', count: 'mine' },
];

interface Props {
  view: HubView;
  onViewChange: (v: HubView) => void;
  search: string;
  onSearchChange: (s: string) => void;
  counts?: HubSegmentCounts;
}

export default function HubInboxToolbar({ view, onViewChange, search, onSearchChange, counts }: Props) {
  const closed = view === 'closed';
  return (
    <div className="flex flex-col gap-3 border-b border-border-subtle px-4 pb-3 pt-3">
      <div className="grid grid-cols-2 gap-1" role="tablist" aria-label="Escopo das conversas">
        {SCOPES.map((sc) => {
          const active = sc.id === 'closed' ? closed : !closed;
          return (
            <button
              key={sc.id}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => onViewChange(sc.id === 'closed' ? 'closed' : 'all')}
              className={clsx(
                'h-8 rounded-control px-2 text-[11px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary',
                active ? 'bg-surface-secondary text-text-primary' : 'text-text-secondary hover:bg-surface-muted',
              )}
            >
              {sc.label}
            </button>
          );
        })}
      </div>
      <label className="relative block">
        <span className="sr-only">Buscar cliente</span>
        <Icon name="search" size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-text-tertiary" />
        <input
          type="search"
          value={search}
          maxLength={80}
          onChange={(e) => onSearchChange(e.target.value)}
          placeholder="Buscar cliente..."
          className="w-full rounded-control border border-transparent bg-surface-muted py-1.5 pl-8 pr-3 text-sm text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-accent-primary"
        />
      </label>
      {!closed && (
        <div className="grid grid-cols-3 rounded-control bg-surface-muted p-1" role="tablist" aria-label="Filtro de conversas">
          {FILTERS.map((f) => {
            const n = counts?.[f.count];
            return (
              <button
                key={f.id}
                type="button"
                role="tab"
                aria-selected={view === f.id}
                aria-label={typeof n === 'number' && n > 0 ? `${f.label} (${n > 99 ? '99+' : n})` : f.label}
                onClick={() => onViewChange(f.id)}
                className={clsx(
                  'flex h-7 items-center justify-center gap-1 rounded-control px-1 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary',
                  view === f.id ? 'bg-surface text-text-primary shadow-sm' : 'text-text-secondary hover:text-text-primary',
                )}
              >
                {f.label}
                {typeof n === 'number' && n > 0 && (
                  <span aria-hidden="true" className={clsx('rounded-pill px-1.5 text-[11px] font-semibold', f.id === 'waiting' ? 'bg-accent-primary text-white' : 'bg-surface-secondary text-text-secondary')}>
                    {n > 99 ? '99+' : n}
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
