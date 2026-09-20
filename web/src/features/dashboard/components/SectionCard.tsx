import type { ReactNode } from 'react'
import clsx from 'clsx'
import { Card, ErrorState } from '../../../components/primitives'

interface Props {
  title: string
  action?: { label: string; onClick: () => void }
  aside?: ReactNode
  /** renders a local ErrorState inside the card instead of tearing down the page */
  error?: string
  onRetry?: () => void
  className?: string
  children: ReactNode
}

export function SectionCard({ title, action, aside, error, onRetry, className, children }: Props) {
  return (
    <Card padding="normal" className={clsx('flex h-full flex-col', className)}>
      <div className="mb-4 flex items-center justify-between gap-4">
        <h2 className="text-section-sm font-semibold text-text-primary">{title}</h2>
        {aside}
        {action && (
          <button
            type="button"
            onClick={action.onClick}
            className="rounded-control text-body-sm font-medium text-accent-primary transition-colors hover:text-accent-primary-hover focus-visible:ring-2 focus-visible:ring-accent-primary"
          >
            {action.label}
          </button>
        )}
      </div>

      <div className="flex-1">
        {error ? (
          <ErrorState
            title="Não foi possível carregar"
            message={error}
            action={onRetry ? { label: 'Tentar novamente', onClick: onRetry } : undefined}
          />
        ) : (
          children
        )}
      </div>
    </Card>
  )
}
