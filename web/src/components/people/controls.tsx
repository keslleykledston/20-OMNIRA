import clsx from 'clsx'
import type { ReactNode } from 'react'
import type { Tone } from '../../lib/peopleModel'

export const CAPACITY_MIN = 1
export const CAPACITY_MAX = 100

// "" or garbage -> 1; anything above the limit -> 100; fractions are rounded.
export function clampCapacity(raw: string | number): number {
  const n = Math.round(Number(raw))
  if (!Number.isFinite(n) || n < CAPACITY_MIN) return CAPACITY_MIN
  return Math.min(CAPACITY_MAX, n)
}

export function Switch({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={clsx(
        'relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary focus-visible:ring-offset-2',
        'disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'border-accent-primary bg-accent-primary' : 'border-border-light bg-surface-muted',
      )}
    >
      <span className={clsx('inline-block h-4 w-4 rounded-full bg-white shadow transition-transform motion-reduce:transition-none', checked ? 'translate-x-6' : 'translate-x-1')} />
    </button>
  )
}

export function CapacityStepper({ value, onChange, label, disabled }: { value: number; onChange: (v: number) => void; label: string; disabled?: boolean }) {
  const step = (d: number) => onChange(clampCapacity(value + d))
  const btn = 'flex h-8 w-8 items-center justify-center text-base font-semibold text-text-primary hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent-primary'
  return (
    <span role="group" aria-label={label} className="inline-flex items-center overflow-hidden rounded-control border border-border-light bg-surface">
      <button type="button" className={btn} aria-label={`Diminuir: ${label}`} disabled={disabled || value <= CAPACITY_MIN} onClick={() => step(-1)}>−</button>
      <output aria-live="polite" className="min-w-[2.25rem] text-center text-sm font-semibold tabular-nums text-text-primary">{value}</output>
      <button type="button" className={btn} aria-label={`Aumentar: ${label}`} disabled={disabled || value >= CAPACITY_MAX} onClick={() => step(1)}>+</button>
    </span>
  )
}

const TONE_PILL: Record<Tone, string> = {
  ok: 'bg-status-success-soft text-status-success border-status-success-border',
  warn: 'bg-status-warning-soft text-status-warning-strong border-status-warning-border',
  info: 'bg-status-info-soft text-status-info border-status-info-border',
  neutral: 'bg-surface-muted text-text-secondary border-border-subtle',
}
const TONE_DOT: Record<Tone, string> = {
  ok: 'bg-status-success',
  warn: 'bg-status-warning',
  info: 'bg-status-info',
  neutral: 'bg-text-tertiary',
}

export function ToneDot({ tone }: { tone: Tone }) {
  return <span aria-hidden="true" className={clsx('inline-block h-2 w-2 shrink-0 rounded-full', TONE_DOT[tone])} />
}

export function TonePill({ tone, children }: { tone: Tone; children: ReactNode }) {
  return (
    <span className={clsx('inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-xs font-semibold', TONE_PILL[tone])}>
      <ToneDot tone={tone} />
      {children}
    </span>
  )
}

export function initialsOf(name: string): string {
  const source = name.includes('@') ? name.split('@')[0] : name
  const parts = source.trim().split(/[\s._-]+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}
