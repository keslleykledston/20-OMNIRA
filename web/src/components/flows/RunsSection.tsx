import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Badge, Card, CardBody, EmptyState, ErrorState, LoadingState, Button, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from '../primitives'
import StableModal from './StableModal'
import { flowErrorMessage, flowsAPI, type RunDetail } from '../../lib/flows'
import { nodeLabel } from '../../lib/flowModel'
import { getTenantId } from '../../lib/session'

const STATUS: Record<string, { label: string; tone: 'default' | 'success' | 'warning' | 'danger' | 'info' }> = {
  running: { label: 'Em execução', tone: 'info' },
  waiting_input: { label: 'Aguardando contato', tone: 'info' },
  waiting_human: { label: 'Com um humano', tone: 'success' },
  completed: { label: 'Concluído', tone: 'success' },
  failed: { label: 'Falhou', tone: 'danger' },
  cancelled: { label: 'Cancelado', tone: 'default' },
  expired: { label: 'Expirado', tone: 'warning' },
}

function fmtDuration(ms: number | null | undefined): string {
  if (ms == null) return '—'
  if (ms < 1000) return `${ms} ms`
  const s = Math.round(ms / 1000)
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60} s`
}

export default function RunsSection() {
  const tenantId = getTenantId()
  const [flowId, setFlowId] = useState('')
  const [status, setStatus] = useState('')
  const [openRun, setOpenRun] = useState<string | null>(null)

  const flows = useQuery({ queryKey: ['flows', tenantId], queryFn: () => flowsAPI.list(), retry: false })
  const runs = useQuery({ queryKey: ['flow-runs', tenantId, flowId, status], queryFn: () => flowsAPI.runs({ flow_id: flowId || undefined, status: status || undefined, limit: 50 }), retry: false, refetchInterval: 15_000 })
  const analytics = useQuery({ queryKey: ['flow-analytics', tenantId, flowId], queryFn: () => flowsAPI.analytics(flowId, 7), enabled: !!flowId, retry: false })
  const detail = useQuery({ queryKey: ['flow-run', tenantId, openRun], queryFn: () => flowsAPI.run(openRun as string), enabled: !!openRun, retry: false })

  const a = analytics.data
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <label className="block">
          <span className="mb-1 block text-xs font-medium text-text-secondary">Fluxo</span>
          <select aria-label="Filtrar por fluxo" className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={flowId} onChange={(e) => setFlowId(e.target.value)}>
            <option value="">Todos</option>
            {(flows.data ?? []).map((f) => <option key={f.id} value={f.id}>{f.name}</option>)}
          </select>
        </label>
        <label className="block">
          <span className="mb-1 block text-xs font-medium text-text-secondary">Situação</span>
          <select aria-label="Filtrar por situação" className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">Todas</option>
            {Object.entries(STATUS).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
          </select>
        </label>
      </div>

      {flowId && a && (
        <section aria-label="Indicadores dos últimos 7 dias" className="space-y-2">
          <h2 className="text-sm font-semibold text-text-primary">Últimos {a.days} dias</h2>
          <dl className="m-0 grid grid-cols-2 gap-3 lg:grid-cols-5">
            {[
              ['Execuções', String(a.runs)],
              ['Concluídas', String(a.completed)],
              ['Falhas', String(a.failed)],
              ['Passaram a um humano', String(a.human_handoffs)],
              ['Duração média', a.avg_duration_ms == null ? '—' : fmtDuration(a.avg_duration_ms)],
            ].map(([label, value]) => (
              <Card key={label}>
                <CardBody>
                  <dt className="text-xs text-text-secondary">{label}</dt>
                  <dd className="m-0 mt-1 text-xl font-semibold text-text-primary">{value}</dd>
                </CardBody>
              </Card>
            ))}
          </dl>
          {a.runs === 0 && <p className="text-xs text-text-tertiary">Ainda não houve execuções neste período: nenhum número é estimado.</p>}
          {a.drop_off_by_node.length > 0 && (
            <p className="text-xs text-text-secondary">Onde execuções falhas ou canceladas pararam: {a.drop_off_by_node.map((d) => `${d.node_id} (${d.count})`).join(', ')}.</p>
          )}
        </section>
      )}

      {runs.isLoading && <LoadingState message="Carregando execuções…" />}
      {runs.isError && <ErrorState message={flowErrorMessage(runs.error)} />}
      {!runs.isLoading && !runs.isError && (runs.data ?? []).length === 0 && <EmptyState title="Nenhuma execução" description="As execuções aparecem aqui quando um fluxo publicado atende uma conversa." />}
      {(runs.data ?? []).length > 0 && (
        <Table>
          <TableHead>
            <TableRow>
              <TableHeaderCell>Fluxo</TableHeaderCell>
              <TableHeaderCell>Situação</TableHeaderCell>
              <TableHeaderCell>Passos</TableHeaderCell>
              <TableHeaderCell>Duração</TableHeaderCell>
              <TableHeaderCell>Início</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {(runs.data ?? []).map((r) => {
              const st = STATUS[r.status] ?? { label: r.status, tone: 'default' as const }
              return (
                <TableRow key={r.id} className="cursor-pointer" onClick={() => setOpenRun(r.id)}>
                  <TableCell><button type="button" className="text-left font-medium text-accent-primary" onClick={(e) => { e.stopPropagation(); setOpenRun(r.id) }}>{r.flow_name} <span className="text-text-tertiary">v{r.flow_version}</span></button></TableCell>
                  <TableCell><Badge variant={st.tone}>{st.label}</Badge></TableCell>
                  <TableCell>{r.node_executions}</TableCell>
                  <TableCell>{fmtDuration(r.duration_ms)}</TableCell>
                  <TableCell>{new Date(r.started_at).toLocaleString('pt-BR')}</TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}

      <StableModal open={!!openRun} title="Detalhe da execução" onClose={() => setOpenRun(null)} footer={<Button onClick={() => setOpenRun(null)}>Fechar</Button>}>
        {detail.isLoading && <LoadingState message="Carregando…" />}
        {detail.isError && <ErrorState message={flowErrorMessage(detail.error)} />}
        {detail.data && <RunTimeline run={detail.data} />}
      </StableModal>
    </div>
  )
}

export function RunTimeline({ run }: { run: RunDetail }) {
  const st = STATUS[run.status] ?? { label: run.status, tone: 'default' as const }
  return (
    <div className="space-y-4" data-testid="run-detail">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={st.tone}>{st.label}</Badge>
        <span className="text-sm text-text-secondary">{run.flow_name} · versão {run.flow_version}</span>
      </div>
      {run.error && <p className="text-sm text-status-danger">{run.error}</p>}
      {run.handoff?.summary && (
        <div className="rounded-control bg-surface-muted p-3">
          <h3 className="text-xs font-semibold text-text-secondary">O que foi entregue ao atendente</h3>
          <p className="mt-1 whitespace-pre-wrap text-sm text-text-primary">{run.handoff.summary}</p>
        </div>
      )}
      <div>
        <h3 className="mb-1 text-xs font-semibold text-text-secondary">Passo a passo</h3>
        <ol className="m-0 list-none space-y-1 p-0">
          {run.timeline.map((s) => (
            <li key={s.seq} className="flex items-start justify-between gap-2 text-sm">
              <span className="text-text-primary">{s.seq}. {nodeLabel(s.node_type)} <span className="text-text-tertiary">({s.node_id}{s.port ? ` → ${s.port}` : ''})</span>
                {s.status === 'failed' && s.error && <span className="text-status-danger"> — {s.error}</span>}
              </span>
              <span className="shrink-0 text-xs text-text-tertiary">{fmtDuration(s.duration_ms)}</span>
            </li>
          ))}
        </ol>
      </div>
      {Object.keys(run.variables).some((k) => !k.startsWith('_')) && (
        <div>
          <h3 className="mb-1 text-xs font-semibold text-text-secondary">Dados coletados</h3>
          <dl className="m-0 space-y-1 text-sm">
            {Object.entries(run.variables).filter(([k, v]) => !k.startsWith('_') && typeof v !== 'object').map(([k, v]) => (
              <div key={k} className="flex justify-between gap-3"><dt className="text-text-secondary">{k}</dt><dd className="break-all text-text-primary">{String(v)}</dd></div>
            ))}
          </dl>
        </div>
      )}
    </div>
  )
}
