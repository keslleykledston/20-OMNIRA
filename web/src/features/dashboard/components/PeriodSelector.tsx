import { useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { Icon } from '../../../components/primitives'
import { PERIODS, type PeriodId } from '../types'

interface Props {
  value: PeriodId
  onChange: (id: PeriodId) => void
  dateLabel: string
}

export function PeriodSelector({ value, onChange, dateLabel }: Props) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const current = PERIODS.find((p) => p.id === value) ?? PERIODS[1]

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="listbox"
        aria-expanded={open}
        className={clsx(
          'flex items-center gap-3 rounded-card border border-border-subtle bg-surface px-3 py-2',
          'text-left transition-colors hover:bg-surface-hover',
          'focus-visible:ring-2 focus-visible:ring-accent-primary',
        )}
      >
        <span className="hidden sm:block">
          <span className="block text-caption text-text-primary">{dateLabel}</span>
          <span className="block text-metadata">{current.label}</span>
        </span>
        <Icon name="calendar" size={18} className="text-text-secondary" />
        <Icon name="chevron-down" size={16} className="text-text-tertiary" />
      </button>

      {open && (
        <ul
          role="listbox"
          className="absolute right-0 z-20 mt-2 w-52 overflow-hidden rounded-card border border-border-subtle bg-surface py-1 shadow-md"
        >
          {PERIODS.map((p) => (
            <li key={p.id}>
              <button
                type="button"
                role="option"
                aria-selected={p.id === value}
                onClick={() => {
                  onChange(p.id)
                  setOpen(false)
                }}
                className={clsx(
                  'w-full px-3 py-2 text-left text-body-sm transition-colors',
                  p.id === value
                    ? 'bg-accent-primary-soft font-semibold text-accent-primary'
                    : 'text-text-secondary hover:bg-surface-hover hover:text-text-primary',
                )}
              >
                {p.label}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
