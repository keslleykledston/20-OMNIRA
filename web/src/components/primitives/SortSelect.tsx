import clsx from 'clsx'

export interface SortOption {
  /** The `sort` the server takes; '' is the default order. */
  value: string
  label: string
}

interface Props {
  options: SortOption[]
  value: string
  onChange: (value: string) => void
  label?: string
  className?: string
}

/**
 * "Ordenar por" for the screens where the table headers are not there (the phone shows cards). Same orderings as the headers; shown from the
 * parent (`md:hidden`), because the headers do the job on a wide screen.
 */
export function SortSelect({ options, value, onChange, label = 'Ordenar por', className }: Props) {
  return (
    <label className={clsx('flex items-center gap-2 text-sm text-text-secondary', className)}>
      {label}
      <select
        aria-label={label}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  )
}
