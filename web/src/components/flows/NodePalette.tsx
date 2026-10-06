import type { NodeTypeInfo } from '../../lib/flows'
import { groupNodeTypes, nodeLabel } from '../../lib/flowModel'

interface Props {
  types: NodeTypeInfo[]
  disabled?: boolean
  hasTrigger: boolean
  onAdd: (type: string) => void
}

const EFFECT_HINT: Record<string, string> = {
  external: 'Envia algo para o contato',
  local: 'Altera dados do OMNIRA',
}

export default function NodePalette({ types, disabled, hasTrigger, onAdd }: Props) {
  const groups = groupNodeTypes(types)
  return (
    <nav aria-label="Paleta de nós" className="space-y-4">
      {groups.map((g) => (
        <section key={g.category}>
          <h3 className="mb-1 px-1 text-xs font-semibold uppercase tracking-wide text-text-tertiary">{g.label}</h3>
          <ul className="m-0 list-none space-y-1 p-0">
            {g.items.map((t) => {
              const blocked = disabled || (t.type === 'trigger' && hasTrigger)
              return (
                <li key={t.type}>
                  <button
                    type="button"
                    disabled={blocked}
                    onClick={() => onAdd(t.type)}
                    title={EFFECT_HINT[t.side_effect] ?? undefined}
                    className="w-full rounded-control border border-border-subtle bg-surface px-3 py-1.5 text-left text-sm text-text-primary hover:bg-surface-muted disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {nodeLabel(t.type)}
                    {t.waits && <span className="ml-1 text-xs text-text-tertiary">· espera resposta</span>}
                  </button>
                </li>
              )
            })}
          </ul>
        </section>
      ))}
    </nav>
  )
}
