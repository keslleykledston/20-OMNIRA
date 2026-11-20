import type { ReactNode, SelectHTMLAttributes, ButtonHTMLAttributes } from 'react'
import clsx from 'clsx'

// Small building blocks shared by the group copilot tabs. They only compose the design tokens already used by the app.

export function Notice({ tone = 'info', children, role }: { tone?: 'info' | 'warning' | 'danger' | 'success'; children: ReactNode; role?: 'alert' | 'status' }) {
  const styles = {
    info: 'bg-status-info-soft text-text-primary border-status-info-border',
    warning: 'bg-status-warning-soft text-text-primary border-status-warning-border',
    danger: 'bg-status-danger-soft text-status-danger border-status-danger-border',
    success: 'bg-status-success-soft text-text-primary border-status-success-border',
  }[tone]
  return (
    <div role={role} className={clsx('rounded-control border px-3 py-2 text-xs leading-relaxed', styles)}>
      {children}
    </div>
  )
}

export function SelectField({ label, children, className, ...props }: SelectHTMLAttributes<HTMLSelectElement> & { label: string; children: ReactNode }) {
  return (
    <select
      {...props}
      aria-label={label}
      className={clsx(
        'h-9 min-w-0 rounded-control border border-border-subtle bg-surface px-2 text-xs text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary disabled:opacity-60',
        className,
      )}
    >
      {children}
    </select>
  )
}

/** A small text button for the actions of a card (the shared Button is sized for forms). */
export function MiniButton({ danger, className, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { danger?: boolean }) {
  return (
    <button
      type="button"
      {...props}
      className={clsx(
        'rounded-control px-2 py-1 text-xs font-medium transition-colors focus-visible:ring-2 focus-visible:ring-accent-primary disabled:cursor-not-allowed disabled:opacity-50',
        danger ? 'text-status-danger hover:bg-status-danger-soft' : 'text-accent-primary hover:bg-accent-primary-soft',
        className,
      )}
    />
  )
}

export function SectionTitle({ children }: { children: ReactNode }) {
  return <h4 className="text-xs font-semibold uppercase tracking-wide text-text-tertiary">{children}</h4>
}

export const fieldClass =
  'w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary placeholder:text-text-tertiary focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary disabled:bg-surface-muted disabled:text-text-tertiary'
