import { useQueries, useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Icon, Skeleton } from '../primitives'
import type { IconName } from '../primitives'
import { queuesAPI, type Queue } from '../../lib/queues'
import { slaAPI } from '../../lib/sectors'
import { getTenantId } from '../../lib/session'

// UX audit M5: which queues need a person's eyes now, from the SLA clocks the server really runs (conversation_sla of the open conversations of each queue).
// Only real numbers: a queue with no policy has no clock and simply never shows as at risk. No invented activity feed (DESIGN.6-B).
type Risk = 'critical' | 'attention'
const RISK: Record<Risk, { label: string; icon: IconName; cls: string }> = {
  critical: { label: 'SLA estourado', icon: 'flag', cls: 'bg-status-danger-soft text-status-danger' },
  attention: { label: 'SLA em risco', icon: 'clock', cls: 'bg-status-warning-soft text-status-warning' },
}
const MAX_QUEUES = 12 // the monitor is one request per queue: a company with more still sees the busiest ones

export default function QueuesAttention() {
  const tenantId = getTenantId()
  const queues = useQuery({ queryKey: ['dashboard-queues', tenantId], queryFn: queuesAPI.list, retry: false })
  const busy: Queue[] = (queues.data ?? []).filter((q) => q.open_conversation_count > 0).sort((a, b) => b.open_conversation_count - a.open_conversation_count).slice(0, MAX_QUEUES)
  const monitors = useQueries({
    queries: busy.map((q) => ({ queryKey: ['att-sla-monitor', tenantId, q.id], queryFn: () => slaAPI.monitor(q.id), retry: false })),
  })

  const status = (queues.error as { response?: { status?: number } } | null)?.response?.status
  // no right to read the queues (agent.read) or the feature absent: the block does not exist
  if (queues.isError && (status === 403 || status === 404)) return null

  const loading = queues.isLoading || monitors.some((m) => m.isLoading)
  const rows = busy
    .map((q, i) => {
      const counts = monitors[i]?.data?.counts ?? {}
      const breached = counts.breached ?? 0
      const atRisk = counts.at_risk ?? 0
      const risk: Risk | null = breached > 0 ? 'critical' : atRisk > 0 ? 'attention' : null
      return { q, breached, atRisk, risk }
    })
    .filter((r): r is typeof r & { risk: Risk } => r.risk !== null)
    .sort((a, b) => (a.risk === b.risk ? b.q.open_conversation_count - a.q.open_conversation_count : a.risk === 'critical' ? -1 : 1))
  const monitorFailed = monitors.some((m) => m.isError)

  return (
    <section aria-labelledby="dash-queues" className="mt-6 overflow-hidden rounded-card border border-border-subtle bg-surface shadow-sm">
      <div className="flex items-center justify-between gap-3 border-b border-border-subtle px-5 py-4 sm:px-6">
        <div>
          <h2 id="dash-queues" className="text-body-md font-bold text-text-primary">Filas que pedem atenção</h2>
          <p className="mt-0.5 text-xs text-text-tertiary">Relógios de SLA das conversas abertas, as mais urgentes primeiro</p>
        </div>
        <Link to="/settings/attendance/overflow" className="text-xs font-medium text-accent-primary hover:underline focus-visible:ring-2 focus-visible:ring-accent-primary">Ver monitor de SLA</Link>
      </div>
      <div className="p-5 sm:p-6">
        {loading && (
          <div aria-busy="true" className="space-y-3">
            <span className="sr-only">Carregando filas</span>
            {[1, 2, 3].map((i) => <Skeleton key={i} width="w-full" height="h-10" />)}
          </div>
        )}
        {!loading && queues.isError && (
          <div role="alert" className="flex flex-wrap items-center gap-3 rounded-control bg-status-danger-soft px-3 py-2 text-sm text-status-danger">
            <span className="flex-1">Não foi possível carregar as filas.</span>
            <button type="button" onClick={() => void queues.refetch()} className="rounded-control border border-status-danger-border bg-surface px-2 py-1 text-xs font-medium text-text-primary hover:bg-surface-hover">Tentar novamente</button>
          </div>
        )}
        {!loading && !queues.isError && rows.length === 0 && (
          <p className="m-0 text-sm text-text-secondary">
            {monitorFailed ? 'Não foi possível ler o relógio de SLA de algumas filas.' : busy.length === 0 ? 'Nenhuma conversa aberta nas filas agora.' : 'Tudo em dia: nenhuma fila com SLA em risco agora.'}
          </p>
        )}
        {!loading && !queues.isError && rows.length > 0 && (
          <ol className="m-0 list-none divide-y divide-border-subtle p-0">
            {rows.map(({ q, breached, atRisk, risk }) => (
              <li key={q.id} className="flex flex-wrap items-center gap-3 py-3 first:pt-0 last:pb-0">
                <div className="min-w-0 flex-1">
                  <p className="m-0 truncate text-sm font-semibold text-text-primary">{q.name}</p>
                  <p className="m-0 text-xs text-text-tertiary">
                    {q.open_conversation_count} {q.open_conversation_count === 1 ? 'conversa aberta' : 'conversas abertas'}
                    {breached > 0 && ` · ${breached} estourada${breached === 1 ? '' : 's'}`}
                    {atRisk > 0 && ` · ${atRisk} em risco`}
                  </p>
                </div>
                {/* the risk is icon + words, never colour alone */}
                <span className={`inline-flex items-center gap-1 rounded-pill px-2 py-0.5 text-xs font-semibold ${RISK[risk].cls}`}>
                  <Icon name={RISK[risk].icon} size={14} />
                  {RISK[risk].label}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </section>
  )
}
