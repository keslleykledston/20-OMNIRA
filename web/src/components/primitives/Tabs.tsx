import { useRef } from 'react'
import clsx from 'clsx'

export interface TabItem<T extends string = string> {
  id: T
  label: string
  disabled?: boolean
}

interface TabsProps<T extends string> {
  items: TabItem<T>[]
  value: T
  onChange: (id: T) => void
  'aria-label': string
  className?: string
}

export function Tabs<T extends string>({
  items,
  value,
  onChange,
  className,
  ...aria
}: TabsProps<T>) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({})

  // Roving focus: arrow keys move between tabs, Home/End jump to the edges.
  const onKeyDown = (e: React.KeyboardEvent) => {
    const enabled = items.filter((i) => !i.disabled)
    const idx = enabled.findIndex((i) => i.id === value)
    let next: number | null = null
    if (e.key === 'ArrowRight') next = (idx + 1) % enabled.length
    else if (e.key === 'ArrowLeft') next = (idx - 1 + enabled.length) % enabled.length
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = enabled.length - 1
    if (next === null) return
    e.preventDefault()
    const target = enabled[next]
    onChange(target.id)
    refs.current[target.id]?.focus()
  }

  return (
    <div
      role="tablist"
      aria-label={aria['aria-label']}
      onKeyDown={onKeyDown}
      className={clsx(
        'flex gap-1 overflow-x-auto border-b border-border-subtle',
        // horizontal scroll on narrow viewports instead of wrapping badly
        '[scrollbar-width:none] [&::-webkit-scrollbar]:hidden',
        className,
      )}
    >
      {items.map((item) => {
        const active = item.id === value
        return (
          <button
            key={item.id}
            ref={(el) => {
              refs.current[item.id] = el
            }}
            role="tab"
            type="button"
            aria-selected={active}
            disabled={item.disabled}
            tabIndex={active ? 0 : -1}
            onClick={() => onChange(item.id)}
            className={clsx(
              'relative whitespace-nowrap px-4 py-3 text-body-sm transition-colors',
              'focus-visible:ring-2 focus-visible:ring-accent-primary',
              'disabled:cursor-not-allowed disabled:text-text-tertiary',
              active
                ? 'font-semibold text-accent-primary'
                : 'text-text-secondary hover:text-text-primary',
            )}
          >
            {item.label}
            {active && (
              <span
                aria-hidden="true"
                className="absolute inset-x-3 -bottom-px h-0.5 rounded-full bg-accent-primary"
              />
            )}
          </button>
        )
      })}
    </div>
  )
}
