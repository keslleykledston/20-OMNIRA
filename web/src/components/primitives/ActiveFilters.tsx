import { Icon } from './Icon'

export interface ActiveFilter {
  key: string
  label: string // "Situação"
  value: string // "Aberto" (what the person sees, never a raw code)
}

/**
 * Chips for the filters that are narrowing a list right now, each one removable, and one "Limpar tudo" for all of them (UX audit M4).
 * It shows nothing when nothing is narrowing the list. The list's own controls stay where they are: this only makes the state visible.
 */
export function ActiveFilters({ filters, onRemove, onClear }: { filters: ActiveFilter[]; onRemove: (key: string) => void; onClear: () => void }) {
  if (filters.length === 0) return null
  return (
    <div role="group" aria-label="Filtros ativos" className="flex flex-wrap items-center gap-1.5">
      {filters.map((f) => (
        <span key={f.key} className="inline-flex items-center gap-1 rounded-pill bg-surface-muted py-0.5 pl-2.5 pr-1 text-xs text-text-primary">
          <span className="text-text-secondary">{f.label}:</span> {f.value}
          <button
            type="button"
            onClick={() => onRemove(f.key)}
            aria-label={`Remover filtro ${f.label}: ${f.value}`}
            className="grid h-5 w-5 place-items-center rounded-full text-text-tertiary hover:bg-surface-hover hover:text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary"
          >
            <Icon name="close" size={12} />
          </button>
        </span>
      ))}
      <button type="button" onClick={onClear} className="rounded-control px-1.5 py-0.5 text-xs font-medium text-accent-primary hover:underline focus-visible:ring-2 focus-visible:ring-accent-primary">
        Limpar tudo
      </button>
    </div>
  )
}
