import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon } from '../primitives'
import { useChannelLines } from '../../lib/channelLines'
import { lineQueuesAPI, queueErrorMessage } from '../../lib/queues'
import { getTenantId } from '../../lib/session'

// ADR-0043 (S2): "Entrada por canal". Each WhatsApp line may name the queue its NEW conversations go to; a line left on the
// default sends them to the default queue. Changing it never moves a conversation that already has a queue.
export function LineEntryRules({ groups, canManage }: { groups: { id: string; name: string; isDefault: boolean }[]; canManage: boolean }) {
  const tenantId = getTenantId()
  const lines = useChannelLines()
  const qc = useQueryClient()
  const rules = useQuery({ queryKey: ['line-queues', tenantId], queryFn: lineQueuesAPI.list, enabled: !!tenantId, retry: false })
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const save = useMutation({
    mutationFn: ({ line, queue }: { line: string; queue: string | null }) => lineQueuesAPI.set(line, queue),
    onMutate: ({ line }) => { setBusy(line); setError(null) },
    onError: (err) => setError(queueErrorMessage(err)),
    onSettled: () => { setBusy(null); qc.invalidateQueries({ queryKey: ['line-queues', tenantId] }) },
  })
  const items = lines.data ?? []
  if (items.length === 0 || groups.length === 0) return null
  const current = new Map((rules.data ?? []).map((r) => [r.connection_id, r.queue_id]))
  const defaultGroup = groups.find((g) => g.isDefault)
  return (
    <section aria-label="Entrada por canal" className="rounded-card border border-border-subtle bg-surface p-4">
      <h3 className="flex items-center gap-2 text-sm font-semibold text-text-primary"><Icon name="channels" size={16} /> Entrada por canal</h3>
      <p className="mt-1 text-xs text-text-secondary">
        Escolha para qual fila as conversas novas de cada número vão. Sem escolha, vão para a fila padrão{defaultGroup ? ` (${defaultGroup.name})` : ''}.
        Conversas que já estão em uma fila não mudam.
      </p>
      {error && <div role="alert" className="mt-2 rounded-control border border-status-danger-border bg-status-danger-soft p-2 text-sm text-status-danger">{error}</div>}
      <ul className="mt-3 divide-y divide-border-subtle">
        {items.map((line) => {
          const value = current.get(line.id) ?? ''
          return (
            <li key={line.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
              <span className="min-w-0 truncate text-sm text-text-primary">{line.label}</span>
              <select
                aria-label={`Fila das conversas novas de ${line.label}`}
                className="rounded-control border border-border-default bg-surface px-2 py-1 text-sm"
                value={value}
                disabled={!canManage || busy === line.id}
                onChange={(e) => save.mutate({ line: line.id, queue: e.target.value === '' ? null : e.target.value })}
              >
                <option value="">Fila padrão{defaultGroup ? ` (${defaultGroup.name})` : ''}</option>
                {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
              </select>
            </li>
          )
        })}
      </ul>
    </section>
  )
}
