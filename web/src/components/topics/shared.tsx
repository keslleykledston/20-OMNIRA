import React from 'react'
import clsx from 'clsx'
import { TopicsError } from '../../lib/topics'

export function SectionTitle({ children }: { children: React.ReactNode }) {
  return <h5 className="text-[11px] font-semibold text-text-tertiary uppercase tracking-wide mb-2">{children}</h5>
}

export function Chip({ tone, children }: { tone: 'neutral' | 'info' | 'success' | 'warning' | 'danger'; children: React.ReactNode }) {
  const tones = {
    neutral: 'bg-surface-muted text-text-secondary',
    info: 'bg-accent-primary-soft text-accent-primary',
    success: 'bg-status-success-soft text-status-success',
    warning: 'bg-status-warning-soft text-status-warning',
    danger: 'bg-status-danger-soft text-status-danger',
  }
  return <span className={clsx('inline-flex items-center rounded-md px-1.5 py-0.5 text-[11px] font-medium', tones[tone])}>{children}</span>
}

export function SmallButton({
  children,
  onClick,
  disabled,
  tone = 'default',
  ...rest
}: {
  children: React.ReactNode
  onClick?: () => void
  disabled?: boolean
  tone?: 'default' | 'primary'
} & Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, 'onClick' | 'disabled' | 'children'>) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={clsx(
        'px-2.5 py-1.5 text-xs font-medium rounded-control border transition-colors',
        tone === 'primary'
          ? 'bg-accent-primary text-white border-accent-primary hover:opacity-90'
          : 'bg-surface text-text-primary border-border-subtle hover:bg-surface-muted',
        disabled && 'opacity-50 cursor-not-allowed'
      )}
      {...rest}
    >
      {children}
    </button>
  )
}

// A feature that is switched off answers 404 and simply is not offered; a refusal says so; anything else is a soft failure.
export function describeError(err: unknown, fallback: string): string {
  const status = err instanceof TopicsError ? err.status : 0
  if (status === 403) return 'Você não tem permissão para esta ação.'
  if (status === 409) return 'O estado mudou; atualize e tente de novo.'
  if (status === 429) return 'Muitas solicitações. Tente de novo em instantes.'
  if (status === 503) return 'Recurso indisponível no momento.'
  if (status === 422) return 'Não há conteúdo suficiente para isso.'
  return fallback
}

export function isOff(err: unknown): boolean {
  return err instanceof TopicsError && err.status === 404
}

export function formatWhen(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
}

export function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}
