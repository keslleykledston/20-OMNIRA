import { useRef } from 'react';
import clsx from 'clsx';
import { Icon } from '../primitives';

// An attendance opened in the unified inbox, kept as a tab above the conversation so the person can move between the ones they are working on
// without searching the list again. The data is what the SERVER showed in the list (names only): the tab authorizes nothing, and opening it asks
// the server again.
export interface OpenAttendance {
  id: string;
  name: string;
  tenantName: string;
  /** Only to narrow the tabs by the company filter of the screen. */
  tenantId: string;
}

interface Props {
  tabs: OpenAttendance[];
  selectedId: string;
  onSelect: (id: string) => void;
  /** Closing a tab only forgets it here: the attendance itself is not finalized, moved or touched. */
  onClose: (id: string) => void;
}

const initials = (name: string) =>
  name.split(/\s+/).filter(Boolean).slice(0, 2).map((p) => p[0]!.toUpperCase()).join('') || '?';

export default function HubOpenTabs({ tabs, selectedId, onSelect, onClose }: Props) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({});
  if (tabs.length === 0) return null;
  // Roving focus like the other tab lists: arrows move, Home/End jump.
  const onKeyDown = (e: React.KeyboardEvent) => {
    const idx = tabs.findIndex((t) => t.id === selectedId);
    let next: number | null = null;
    if (e.key === 'ArrowRight') next = (idx + 1) % tabs.length;
    else if (e.key === 'ArrowLeft') next = (idx - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = tabs.length - 1;
    if (next === null) return;
    e.preventDefault();
    onSelect(tabs[next].id);
    refs.current[tabs[next].id]?.focus();
  };
  return (
    <div className="shrink-0 border-b border-border-subtle bg-surface">
      <p className="flex items-center gap-1.5 px-4 pb-1 pt-2.5 text-[10px] font-semibold uppercase tracking-wide text-text-tertiary">
        <Icon name="conversations" size={12} />
        Atendimentos abertos
      </p>
      <div role="tablist" aria-label="Atendimentos abertos" onKeyDown={onKeyDown} className="flex gap-1 overflow-x-auto px-2 pb-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {tabs.map((t) => {
          const active = t.id === selectedId;
          return (
            <div key={t.id} className="relative shrink-0">
              <button
                ref={(el) => {
                  refs.current[t.id] = el;
                }}
                type="button"
                role="tab"
                aria-selected={active}
                tabIndex={active ? 0 : -1}
                onClick={() => onSelect(t.id)}
                className={clsx(
                  'flex h-12 min-w-40 max-w-52 items-center gap-2 rounded-control border py-1 pl-2 pr-9 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary',
                  active ? 'border-accent-primary/40 bg-accent-primary-soft' : 'border-border-subtle bg-surface-muted hover:bg-surface-secondary',
                )}
              >
                <span aria-hidden="true" className="grid size-7 shrink-0 place-items-center rounded-full bg-surface-secondary text-[10px] font-semibold text-text-secondary">
                  {initials(t.name)}
                </span>
                <span className="min-w-0">
                  <span className="block truncate text-[11px] font-semibold text-text-primary">{t.name}</span>
                  <span className="block truncate text-[10px] text-text-secondary">{t.tenantName}</span>
                </span>
              </button>
              <button
                type="button"
                onClick={() => onClose(t.id)}
                aria-label={`Fechar aba de ${t.name}`}
                title="Fechar aba sem finalizar o atendimento"
                className="absolute right-1 top-1/2 grid size-7 -translate-y-1/2 place-items-center rounded-control text-text-tertiary hover:bg-surface-secondary hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary"
              >
                <Icon name="close" size={12} />
              </button>
            </div>
          );
        })}
      </div>
    </div>
  );
}
