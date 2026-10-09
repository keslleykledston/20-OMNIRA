import { useEffect, useRef, useState } from 'react';
import clsx from 'clsx';
import type { HubCompanyOption } from '../../lib/hub';

interface Props {
  companies: HubCompanyOption[];
  /** Selected company ids; empty means "all the companies I serve". */
  value: string[];
  onChange: (ids: string[]) => void;
}

export function filterSummary(companies: HubCompanyOption[], value: string[]): string {
  const known = value.filter((id) => companies.some((c) => c.id === id));
  if (known.length === 0 || known.length === companies.length) return 'Todas as instâncias';
  if (known.length === 1) return companies.find((c) => c.id === known[0])?.name ?? '1 instância';
  return `${known.length} instâncias`;
}

// The drop-down that replaces the channel selector in the unified conversations view: all the instances the person is
// authorized to serve, or one or more of them. A screen filter only: the server decides what is readable.
export default function CompanyFilter({ companies, value, onChange }: Props) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => { if (root.current && !root.current.contains(e.target as Node)) setOpen(false); };
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', esc);
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', esc); };
  }, [open]);

  const selected = new Set(value.filter((id) => companies.some((c) => c.id === id)));
  const all = selected.size === 0 || selected.size === companies.length;
  const toggle = (id: string) => {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    // everything ticked is the same as "all": keep the state canonical (empty) so a new company shows up by default
    onChange(next.size === companies.length ? [] : [...next]);
  };

  return (
    <div ref={root} className="relative">
      <button type="button" aria-haspopup="true" aria-expanded={open} aria-label="Filtrar por instância" onClick={() => setOpen((o) => !o)}
        className="flex h-9 w-full max-w-[16rem] items-center justify-between gap-2 rounded-control border border-border-light bg-surface px-2.5 text-sm font-medium text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary">
        <span className="truncate">{filterSummary(companies, value)}</span>
        <span aria-hidden className="text-text-tertiary">▾</span>
      </button>
      {open && (
        <div role="group" aria-label="Instâncias" className="absolute left-0 z-20 mt-1 w-72 max-w-[calc(100vw-2rem)] rounded-control border border-border-light bg-surface p-1 shadow-lg">
          <label className={clsx('flex cursor-pointer items-center gap-2 rounded-control px-2 py-1.5 text-sm hover:bg-surface-muted', all && 'font-semibold')}>
            <input type="checkbox" checked={all} onChange={() => onChange([])} />
            <span>Todas as instâncias</span>
          </label>
          <div className="my-1 border-t border-border-subtle" />
          <ul className="max-h-64 overflow-y-auto">
            {companies.map((c) => (
              <li key={c.id}>
                <label className="flex cursor-pointer items-center gap-2 rounded-control px-2 py-1.5 text-sm hover:bg-surface-muted">
                  <input type="checkbox" checked={!all && selected.has(c.id)} onChange={() => toggle(c.id)} aria-label={c.name} />
                  <span className="truncate">{c.name}</span>
                </label>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
