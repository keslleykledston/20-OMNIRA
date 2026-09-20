import { useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { Icon } from './Icon'

export interface MenuAction {
  label: string
  onSelect: () => void
  destructive?: boolean
  disabled?: boolean
}

interface Props {
  actions: MenuAction[]
  label?: string
  align?: 'left' | 'right'
}

export function DropdownMenu({ actions, label = 'Ações', align = 'right' }: Props) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const itemRefs = useRef<(HTMLButtonElement | null)[]>([])

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        triggerRef.current?.focus()
      }
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const onMenuKeyDown = (e: React.KeyboardEvent, index: number) => {
    const usable = actions.map((a, i) => (a.disabled ? -1 : i)).filter((i) => i >= 0)
    const pos = usable.indexOf(index)
    let next: number | null = null
    if (e.key === 'ArrowDown') next = usable[(pos + 1) % usable.length]
    else if (e.key === 'ArrowUp') next = usable[(pos - 1 + usable.length) % usable.length]
    if (next === null) return
    e.preventDefault()
    itemRefs.current[next]?.focus()
  }

  return (
    <div ref={ref} className="relative">
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        onClick={() => setOpen((v) => !v)}
        className={clsx(
          'rounded-control p-2 text-text-secondary transition-colors',
          'hover:bg-surface-hover hover:text-text-primary',
          'focus-visible:ring-2 focus-visible:ring-accent-primary',
        )}
      >
        <Icon name="more" size={20} />
      </button>

      {open && (
        <div
          role="menu"
          className={clsx(
            'absolute z-30 mt-1 w-52 overflow-hidden rounded-card border border-border-subtle bg-surface py-1 shadow-md',
            align === 'right' ? 'right-0' : 'left-0',
          )}
        >
          {actions.map((action, i) => (
            <button
              key={action.label}
              ref={(el) => {
                itemRefs.current[i] = el
              }}
              role="menuitem"
              type="button"
              disabled={action.disabled}
              onKeyDown={(e) => onMenuKeyDown(e, i)}
              onClick={() => {
                setOpen(false)
                action.onSelect()
              }}
              className={clsx(
                'block w-full px-3 py-2 text-left text-body-sm transition-colors',
                'focus-visible:bg-surface-hover focus-visible:outline-none',
                'disabled:cursor-not-allowed disabled:text-text-tertiary',
                action.destructive
                  ? 'text-status-danger hover:bg-status-danger-soft'
                  : 'text-text-secondary hover:bg-surface-hover hover:text-text-primary',
              )}
            >
              {action.label}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
