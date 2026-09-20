import clsx from 'clsx'
import { Icon } from '../../../components/primitives'

export interface WizardStep {
  id: number
  label: string
}

interface Props {
  steps: WizardStep[]
  current: number
}

export function WizardStepper({ steps, current }: Props) {
  return (
    <ol className="flex items-center gap-2" aria-label="Progresso da conexão">
      {steps.map((step, i) => {
        const done = step.id < current
        const active = step.id === current
        return (
          <li key={step.id} className="flex flex-1 items-center gap-2">
            <span
              className="flex items-center gap-2"
              aria-current={active ? 'step' : undefined}
            >
              <span
                className={clsx(
                  'flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full text-caption font-semibold transition-colors',
                  done && 'bg-accent-primary text-white',
                  active && 'bg-accent-primary text-white',
                  !done && !active && 'bg-surface-muted text-text-tertiary',
                )}
              >
                {done ? <Icon name="check" size={15} /> : step.id}
              </span>
              <span
                className={clsx(
                  'hidden whitespace-nowrap text-body-sm sm:inline',
                  active ? 'font-semibold text-text-primary' : 'text-text-secondary',
                )}
              >
                {step.label}
              </span>
              {/* screen readers get the state, not just the colour */}
              <span className="sr-only">
                {done ? ' (concluído)' : active ? ' (etapa atual)' : ' (pendente)'}
              </span>
            </span>
            {i < steps.length - 1 && (
              <span
                aria-hidden="true"
                className={clsx(
                  'h-px flex-1',
                  step.id < current ? 'bg-accent-primary' : 'bg-border-subtle',
                )}
              />
            )}
          </li>
        )
      })}
    </ol>
  )
}
