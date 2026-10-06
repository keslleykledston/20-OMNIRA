import clsx from 'clsx'
import type { Issue } from '../../lib/flows'

interface Props {
  issues: Issue[]
  checking?: boolean
  onSelectNode: (id: string) => void
}

export default function IssuesPanel({ issues, checking, onSelectNode }: Props) {
  const errors = issues.filter((i) => i.severity === 'error')
  const warnings = issues.filter((i) => i.severity === 'warning')
  return (
    <section aria-label="Problemas encontrados" className="space-y-2">
      <header className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-text-primary">Problemas</h2>
        <span className="text-xs text-text-tertiary" aria-live="polite">
          {checking ? 'Verificando…' : errors.length === 0 && warnings.length === 0 ? 'Nenhum problema' : `${errors.length} erro(s), ${warnings.length} aviso(s)`}
        </span>
      </header>
      {errors.length === 0 && warnings.length === 0 && !checking && <p className="text-xs text-text-secondary">O fluxo está pronto para ser publicado.</p>}
      <ul className="m-0 list-none space-y-1 p-0">
        {[...errors, ...warnings].map((i, idx) => (
          <li key={`${i.code}-${i.node_id ?? ''}-${idx}`}>
            <button
              type="button"
              disabled={!i.node_id}
              onClick={() => i.node_id && onSelectNode(i.node_id)}
              className={clsx(
                'w-full rounded-control border px-2 py-1.5 text-left text-xs',
                i.severity === 'error' ? 'border-status-danger-border bg-status-danger-soft text-text-primary' : 'border-status-warning-border bg-status-warning-soft text-text-primary',
                i.node_id ? 'cursor-pointer' : 'cursor-default',
              )}
            >
              <span className="mr-1 font-semibold">{i.severity === 'error' ? 'Erro:' : 'Aviso:'}</span>
              {i.message}
            </button>
          </li>
        ))}
      </ul>
    </section>
  )
}
