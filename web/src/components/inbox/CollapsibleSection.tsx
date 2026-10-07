import { useState, type ReactNode } from 'react'
import clsx from 'clsx'
import { Icon, type IconName } from '../primitives'

const STORE = 'omnira.inbox.section.'

function remembered(id: string, fallback: boolean): boolean {
  try {
    const v = localStorage.getItem(STORE + id)
    return v === null ? fallback : v === '1'
  } catch {
    return fallback
  }
}

interface Props {
  /** Stable key: the open/closed choice is remembered per viewer under it. */
  id: string
  title: string
  icon?: IconName
  defaultOpen?: boolean
  children: ReactNode
}

/**
 * A collapsible group of the context pane (native <details>: keyboard and screen reader work for free). The group's
 * own children keep their data and behaviour; their first heading is read by screen readers but not repeated on screen,
 * since the section title already names it.
 */
export function CollapsibleSection({ id, title, icon, defaultOpen = false, children }: Props) {
  const [open, setOpen] = useState(() => remembered(id, defaultOpen))
  return (
    <details
      open={open}
      onToggle={(e) => {
        const next = (e.currentTarget as HTMLDetailsElement).open
        setOpen(next)
        try {
          localStorage.setItem(STORE + id, next ? '1' : '0')
        } catch {
          /* the choice just is not remembered */
        }
      }}
      className="group border-b border-border-subtle"
    >
      <summary
        className={clsx(
          'flex min-h-11 cursor-pointer list-none items-center gap-2 px-4 text-[11px] font-semibold uppercase text-text-tertiary',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent-primary [&::-webkit-details-marker]:hidden',
        )}
      >
        {icon && <Icon name={icon} size={14} />}
        <span>{title}</span>
        <Icon name="chevron-down" size={14} className="ml-auto transition-transform group-open:rotate-180 motion-reduce:transition-none" />
      </summary>
      <div
        className={clsx(
          // children bring their own padding, border and first heading: the section owns those now
          '[&>*]:!border-b-0 [&>*]:!pt-0 [&_h3:first-of-type]:sr-only [&>*>h4:first-child]:sr-only [&>*>h3:first-child]:sr-only',
        )}
      >
        {children}
      </div>
    </details>
  )
}
