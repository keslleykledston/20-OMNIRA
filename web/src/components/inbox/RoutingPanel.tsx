import { useQuery } from '@tanstack/react-query'
import { Badge, Skeleton, StatusBadge } from '../primitives'
import { routingAPI } from '../../lib/sectors'
import { DECISION_LABELS, ENGINE_STATE_LABELS, METRIC_LABELS, humanDuration, reasonLabel, slaStatus } from '../../lib/attendanceModel'

function when(iso?: string, tz?: string): string {
  if (!iso) return ''
  try {
    return new Date(iso).toLocaleString('pt-BR', { weekday: 'short', day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: tz || undefined })
  } catch {
    return new Date(iso).toLocaleString('pt-BR')
  }
}

// ADR-0044: onde a conversa está (setor, grupo, responsável), por que espera, quando pode mudar, as metas de SLA e o histórico de decisões.
// Só leitura. Sem permissão ou sem motor ligado, mostra o que houver (setor/grupo/horário) e some o resto.
export default function RoutingPanel({ conversationId }: { conversationId: string }) {
  const q = useQuery({ queryKey: ['conv-routing', conversationId], queryFn: () => routingAPI.conversation(conversationId), retry: false, refetchInterval: 30_000 })
  if (q.isLoading) return <Skeleton className="m-4 h-20" />
  if (q.isError || !q.data) return <p className="px-4 pb-4 text-xs text-text-tertiary">O roteamento desta conversa não está disponível agora.</p>
  const r = q.data
  const now = new Date()
  const tz = r.schedule?.timezone
  return (
    <section aria-label="Roteamento" className="space-y-3 px-4 pb-4 text-sm">
      <dl className="space-y-1">
        <div className="flex justify-between gap-3"><dt className="text-text-secondary">Setor</dt><dd className="text-right text-text-primary">{r.sector?.name ?? 'Sem setor'}</dd></div>
        <div className="flex justify-between gap-3"><dt className="text-text-secondary">Grupo</dt><dd className="text-right text-text-primary">{r.queue?.name ?? 'Sem grupo'}</dd></div>
        <div className="flex justify-between gap-3"><dt className="text-text-secondary">Atendente</dt><dd className="text-right text-text-primary">{r.assigned ? r.assigned.name || 'Atribuída' : 'Ninguém ainda'}</dd></div>
      </dl>

      {r.schedule && (
        <p>
          {r.schedule.open
            ? <StatusBadge status="success" size="sm">Horário: aberto{r.schedule.next_close ? ` até ${when(r.schedule.next_close, tz)}` : ''}</StatusBadge>
            : <StatusBadge status="warning" size="sm">Horário: fechado{r.schedule.next_open ? ` · abre ${when(r.schedule.next_open, tz)}` : ''}</StatusBadge>}
        </p>
      )}

      {r.engine && (
        <div className="rounded-control bg-surface-muted p-2">
          <p className="font-medium text-text-primary">{ENGINE_STATE_LABELS[r.engine.state] ?? r.engine.state}</p>
          {r.status === 'open' && !r.assigned && typeof r.engine.waiting_seconds === 'number' && <p className="text-xs text-text-secondary">Espera: {humanDuration(r.engine.waiting_seconds)}</p>}
          {r.engine.next_attempt_at && <p className="text-xs text-text-secondary">Próxima tentativa: {when(r.engine.next_attempt_at, tz)}</p>}
          {r.engine.hops > 0 && <p className="text-xs text-text-secondary">Já passou por {r.engine.hops} {r.engine.hops === 1 ? 'etapa' : 'etapas'} de transbordo.</p>}
        </div>
      )}

      {r.sla.length > 0 && (
        <div>
          <h4 className="mb-1 text-xs font-semibold uppercase text-text-tertiary">Metas de SLA</h4>
          <ul className="space-y-1">
            {r.sla.map((c) => {
              const st = slaStatus(c, now)
              return (
                <li key={`${c.metric}${c.started_at}`} className="flex items-center justify-between gap-2">
                  <span>{METRIC_LABELS[c.metric] ?? c.metric}</span>
                  <StatusBadge status={st.tone} size="sm">{st.text}</StatusBadge>
                </li>
              )
            })}
          </ul>
        </div>
      )}

      {r.history.length > 0 && (
        <details>
          <summary className="cursor-pointer text-xs font-semibold uppercase text-text-tertiary">Histórico de roteamento ({r.history.length})</summary>
          <ol className="mt-2 space-y-2">
            {r.history.map((h, i) => (
              <li key={`${h.at}${i}`} className="text-xs">
                <span className="font-medium text-text-primary">{DECISION_LABELS[h.decision] ?? h.decision}</span>
                {h.destination_label && <> → {h.destination_label}</>}
                <span className="text-text-secondary"> · {reasonLabel(h.reason)}</span>
                {h.attempts > 1 && <Badge size="sm" className="ml-1">{h.attempts}×</Badge>}
                <div className="text-text-tertiary">{new Date(h.at).toLocaleString('pt-BR')}</div>
              </li>
            ))}
          </ol>
        </details>
      )}
    </section>
  )
}
