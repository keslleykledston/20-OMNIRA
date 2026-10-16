import clsx from 'clsx'
import { Icon, type IconName } from './Icon'

type Tone = 'neutral' | 'primary' | 'danger'

interface Props extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, 'children' | 'aria-label'> {
  icon: IconName
  /** What it does. It is the accessible name AND the tooltip: an icon alone never says it. */
  label: string
  tone?: Tone
  /** Pressed / selected state (a toggle). */
  pressed?: boolean
}

const TONE: Record<Tone, string> = {
  neutral: 'border-border-subtle bg-surface text-text-secondary hover:bg-surface-hover hover:text-text-primary',
  primary: 'border-transparent bg-accent-primary text-white hover:bg-accent-primary-hover',
  danger: 'border-border-subtle bg-status-danger-soft text-status-danger hover:bg-status-danger-border',
}

// A compact icon-only action (32px) for dense panes: the label is the tooltip and the accessible name, so the screen reader and the tests still find it by what it does.
export function ActionIcon({ icon, label, tone = 'neutral', pressed, className, type = 'button', ...props }: Props) {
  return (
    <button
      {...props}
      type={type}
      aria-label={label}
      title={label}
      aria-pressed={pressed}
      className={clsx(
        'inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-control border transition-colors',
        'focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary',
        'disabled:cursor-not-allowed disabled:opacity-50',
        TONE[tone],
        className,
      )}
    >
      <Icon name={icon} size={16} />
    </button>
  )
}
