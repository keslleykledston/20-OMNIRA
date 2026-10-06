import { Badge, Button } from '../primitives'
import type { Flow, FlowVersion } from '../../lib/flows'

interface Props {
  flow: Flow
  versions: FlowVersion[]
  canPublish: boolean
  busy?: boolean
  onActivate: (version: number) => void
}

export default function VersionsPanel({ flow, versions, canPublish, busy, onActivate }: Props) {
  return (
    <section aria-label="Versões publicadas" className="space-y-2">
      <header>
        <h2 className="text-sm font-semibold text-text-primary">Versões</h2>
        <p className="text-xs text-text-secondary">Cada publicação cria uma versão que nunca muda. Voltar a uma versão antiga só afeta as próximas conversas.</p>
      </header>
      {versions.length === 0 && <p className="text-xs text-text-secondary">Este fluxo ainda não foi publicado.</p>}
      <ul className="m-0 list-none space-y-2 p-0">
        {versions.map((v) => {
          const active = flow.active_version === v.version
          return (
            <li key={v.id} className="flex items-start justify-between gap-2 rounded-control border border-border-subtle p-2">
              <div className="min-w-0">
                <p className="text-sm font-medium text-text-primary">
                  Versão {v.version} {active && <Badge variant="success" size="sm">ativa</Badge>}
                </p>
                <p className="text-xs text-text-tertiary">{new Date(v.published_at).toLocaleString('pt-BR')}</p>
                {v.note && <p className="mt-1 text-xs text-text-secondary">{v.note}</p>}
              </div>
              {canPublish && !active && (
                <Button type="button" size="sm" variant="secondary" disabled={busy} onClick={() => onActivate(v.version)} aria-label={`Ativar a versão ${v.version}`}>
                  Ativar
                </Button>
              )}
            </li>
          )
        })}
      </ul>
    </section>
  )
}
