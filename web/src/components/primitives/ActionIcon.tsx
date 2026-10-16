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

// A compact icon-only action (32px) for dense panes. The label is the accessible name AND a caption that shows at once on hover or keyboard focus (the browser's own
// title tooltip waits a second and is not shown to keyboard users). The caption hangs under the button, aligned to its right edge, so it never leaves the narrow pane.
export function ActionIcon({ icon, label, tone = 'neutral', pressed, className, type = 'button', ...props }: Props) {
  return (
    <span className="group relative inline-flex flex-shrink-0">
      <button
        {...props}
        type={type}
        aria-label={label}
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
      <span
        data-tooltip=""
        aria-hidden="true"
        className={clsx(
          'pointer-events-none absolute right-0 top-full z-50 mt-1 hidden whitespace-nowrap rounded-control bg-text-primary px-2 py-1 text-[11px] font-medium text-surface shadow-lg',
          'group-hover:block group-focus-within:block',
        )}
      >
        {label}
      </span>
    </span>
  )
}
