import { useEffect, useState } from 'react'
import { capacityText } from '../lib/personDraft'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Input, Modal, Skeleton, StatusBadge } from '../components/primitives'
import { AttendanceFrame, Field, Notice, selectClass } from '../components/attendance/AttendanceFrame'
import { agentsAPI } from '../lib/agents'
import { queuesAPI } from '../lib/queues'
import { attendanceErrorMessage, calendarsAPI, hubContractsAPI, overflowAPI, sectorsAPI, shiftsAPI, slaAPI, type OverflowPolicy, type OverflowStep, type SLAPolicy, type SLAScope } from '../lib/sectors'
import { DEST_LABELS, EXHAUSTED_LABELS, TRIGGER_LABELS, describeWait, emptyStep, formatSeconds, moveStep, policyProblems, renumber } from '../lib/attendanceModel'
import { flowsAPI } from '../lib/flows'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'

const blankPolicy = (): OverflowPolicy => ({ active: true, max_steps: 4, on_exhausted: 'manual', steps: [] })

function WaitInput({ step, disabled, onChange }: { step: OverflowStep; disabled: boolean; onChange: (s: Partial<OverflowStep>) => void }) {
  const unit = step.wait_seconds % 3600 === 0 && step.wait_seconds > 0 ? 3600 : 60
  const value = step.wait_seconds / unit
  return (
    <div className="flex flex-wrap items-end gap-2">
      <Input label={`Esperar antes de agir (etapa ${step.position})`} type="number" min={0} value={String(value)} disabled={disabled}
        onChange={(e) => onChange({ wait_seconds: Math.max(0, Number(e.target.value) || 0) * unit })} />
      <select aria-label={`Unidade da espera (etapa ${step.position})`} className={`${selectClass} max-w-[120px]`} value={unit} disabled={disabled}
        onChange={(e) => onChange({ wait_seconds: value * Number(e.target.value) })}>
        <option value={60}>minutos</option>
        <option value={3600}>horas</option>
      </select>
      <label className="flex items-center gap-2 pb-2 text-sm"><input type="checkbox" checked={step.wait_business} disabled={disabled} onChange={(e) => onChange({ wait_business: e.target.checked })} /> Contar só horário útil</label>
    </div>
  )
}

function PolicyEditor({ queueId, manage }: { queueId: string; manage: boolean }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const access = useAccess()
  const canOverride = access.can('routing.override')
  const [draft, setDraft] = useState<OverflowPolicy>(blankPolicy())
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const [removing, setRemoving] = useState(false)
  const policy = useQuery({ queryKey: ['att-policy', tenantId, queueId], queryFn: () => overflowAPI.get(queueId), retry: false })
  const queues = useQuery({ queryKey: ['att-queues', tenantId], queryFn: queuesAPI.list, retry: false })
  const sectors = useQuery({ queryKey: ['att-sectors', tenantId], queryFn: sectorsAPI.list, retry: false })
  const rosters = useQuery({ queryKey: ['att-rosters', tenantId], queryFn: shiftsAPI.rosters, retry: false })
  const agents = useQuery({ queryKey: ['att-agents', tenantId], queryFn: agentsAPI.list, retry: false })
  const contracts = useQuery({ queryKey: ['att-contracts', tenantId], queryFn: hubContractsAPI.list, retry: false, enabled: manage })
  const flows = useQuery({ queryKey: ['att-flows', tenantId], queryFn: () => flowsAPI.list({ status: 'published' }), retry: false, enabled: manage })

  useEffect(() => {
    if (policy.data !== undefined) setDraft(policy.data ? { ...policy.data, steps: renumber(policy.data.steps) } : blankPolicy())
  }, [policy.data])

  const problems = policyProblems(draft, queueId)
  const save = useMutation({
    mutationFn: () => overflowAPI.put(queueId, draft, policy.data?.revision),
    onSuccess: async () => { setSaved(true); setError(null); await qc.invalidateQueries({ queryKey: ['att-policy', tenantId, queueId] }) },
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const remove = useMutation({
    mutationFn: () => overflowAPI.remove(queueId),
    onSuccess: async () => { await qc.invalidateQueries({ queryKey: ['att-policy', tenantId, queueId] }) },
    onError: (e) => setError(attendanceErrorMessage(e)),
  })

  const patch = (i: number, p: Partial<OverflowStep>) => setDraft({ ...draft, steps: draft.steps.map((s, k) => (k === i ? { ...s, ...p } : s)) })
  const destinations = (kind: OverflowStep['destination_kind']): { id: string; label: string }[] => {
    switch (kind) {
      case 'queue': return (queues.data ?? []).filter((q) => q.id !== queueId).map((q) => ({ id: q.id, label: q.name }))
      case 'sector': return (sectors.data ?? []).filter((s) => s.active).map((s) => ({ id: s.id, label: s.name }))
      case 'oncall': return (rosters.data ?? []).filter((r) => r.active).map((r) => ({ id: r.id, label: `${r.name}${r.on_now ? ` (${r.on_now} de plantão)` : ' (ninguém de plantão agora)'}` }))
      case 'agent': return (agents.data ?? []).filter((a) => a.status === 'active').map((a) => ({ id: a.user_id, label: a.name || a.email }))
      case 'hub': return (contracts.data ?? []).map((c) => ({ id: c.contract_id, label: `${c.hub_name}${c.status === 'active' ? '' : ` (${c.status})`}` }))
      case 'flow': return (flows.data ?? []).filter((f) => f.type === 'INBOUND' || f.type === 'AFTER_HOURS').map((f) => ({ id: f.id, label: `${f.name}${f.type === 'AFTER_HOURS' ? ' (fora do horário)' : ''}` }))
    }
  }
  const toggleTrigger = (i: number, t: string) => {
    const has = draft.steps[i].triggers.includes(t)
    patch(i, { triggers: has ? draft.steps[i].triggers.filter((x) => x !== t) : [...draft.steps[i].triggers, t] })
  }
  const addStep = () => {
    const first = destinations('queue')[0]
    setDraft({ ...draft, steps: renumber([...draft.steps, emptyStep(draft.steps.length + 1, { destination_kind: 'queue', destination_id: first?.id ?? '' })]) })
  }

  if (policy.isLoading) return <Skeleton className="h-40" />
  if (policy.isError) return <ErrorState message="Não foi possível carregar a política de transbordo." action={{ label: 'Tentar novamente', onClick: () => { void policy.refetch() } }} />
  const ro = !manage

  return (
    <div className="space-y-4">
      {!policy.data && <Notice>Esta fila não tem transbordo: a conversa espera aqui até alguém atender, como sempre foi. Monte uma cadeia de etapas para movê-la quando a fila estiver fechada, sem atendente, lotado ou com SLA em risco.</Notice>}
      {error && <ErrorState title="Não foi possível salvar" message={error} isDismissible onDismiss={() => setError(null)} />}
      {saved && <Notice>Política salva. As conversas que estavam esperando nesta fila foram reavaliadas com a nova política.</Notice>}
      <div className="grid gap-3 sm:grid-cols-3">
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={draft.active} disabled={ro} onChange={(e) => setDraft({ ...draft, active: e.target.checked })} /> Política ativa</label>
        <Input label="Limite de etapas por conversa" type="number" min={1} max={8} value={String(draft.max_steps)} disabled={ro} onChange={(e) => setDraft({ ...draft, max_steps: Number(e.target.value) || 1 })} />
        <Field label="Quando acabarem as etapas">
          <select className={selectClass} value={draft.on_exhausted} disabled={ro} onChange={(e) => setDraft({ ...draft, on_exhausted: e.target.value as OverflowPolicy['on_exhausted'] })}>
            {Object.entries(EXHAUSTED_LABELS).map(([k, l]) => <option key={k} value={k}>{l}</option>)}
          </select>
        </Field>
      </div>

      <ol className="space-y-3">
        {draft.steps.map((s, i) => (
          <li key={i}>
            <Card padding="compact" aria-label={`Etapa ${s.position}`}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h4 className="text-sm font-semibold text-text-primary">Etapa {s.position}: {DEST_LABELS[s.destination_kind]} — {describeWait(s.wait_seconds, s.wait_business)}</h4>
                {!ro && (
                  <div className="flex gap-2">
                    <Button size="sm" variant="secondary" disabled={i === 0} onClick={() => setDraft({ ...draft, steps: moveStep(draft.steps, i, i - 1) })}>Subir</Button>
                    <Button size="sm" variant="secondary" disabled={i === draft.steps.length - 1} onClick={() => setDraft({ ...draft, steps: moveStep(draft.steps, i, i + 1) })}>Descer</Button>
                    <Button size="sm" variant="secondary" onClick={() => setDraft({ ...draft, steps: renumber(draft.steps.filter((_, k) => k !== i)) })}>Remover etapa</Button>
                  </div>
                )}
              </div>
              <fieldset className="mt-2">
                <legend className="text-xs font-medium text-text-secondary">Quando agir (qualquer um destes)</legend>
                <div className="mt-1 flex flex-wrap gap-3">
                  {Object.entries(TRIGGER_LABELS).map(([t, l]) => (
                    <label key={t} className="flex items-center gap-1 text-sm"><input type="checkbox" checked={s.triggers.includes(t)} disabled={ro} onChange={() => toggleTrigger(i, t)} /> {l}</label>
                  ))}
                </div>
              </fieldset>
              <div className="mt-2"><WaitInput step={s} disabled={ro} onChange={(p) => patch(i, p)} /></div>
              <div className="mt-2 grid gap-2 sm:grid-cols-2">
                <Field label={`Destino da etapa ${s.position}`}>
                  <select className={selectClass} value={s.destination_kind} disabled={ro} onChange={(e) => patch(i, { destination_kind: e.target.value as OverflowStep['destination_kind'], destination_id: '', destination_queue_id: undefined })}>
                    {(Object.keys(DEST_LABELS) as OverflowStep['destination_kind'][]).map((k) => <option key={k} value={k}>{DEST_LABELS[k]}</option>)}
                  </select>
                </Field>
                <Field label={`${DEST_LABELS[s.destination_kind]}`}>
                  <select aria-label={`Escolha o destino da etapa ${s.position}`} className={selectClass} value={s.destination_id} disabled={ro} onChange={(e) => patch(i, { destination_id: e.target.value })}>
                    <option value="">Escolha…</option>
                    {destinations(s.destination_kind).map((d) => <option key={d.id} value={d.id}>{d.label}</option>)}
                  </select>
                </Field>
                {s.destination_kind === 'flow' && (
                  <p className="text-xs text-text-tertiary sm:col-span-2">O fluxo é iniciado na própria conversa, que continua na fila. Só inicia se nenhum fluxo estiver em execução nela; caso contrário a etapa é pulada e a cadeia segue. O fluxo escolhe o que fazer em seguida (por exemplo, “Aplicar transbordo” ou “Transferir para humano”).</p>
                )}
                {s.destination_kind === 'hub' && (
                  <Field label="Fila da empresa que o Hub atende" hint="A conversa continua dentro da sua empresa; o Hub só a atende por esta fila, com as permissões do contrato.">
                    <select className={selectClass} value={s.destination_queue_id ?? ''} disabled={ro} onChange={(e) => patch(i, { destination_queue_id: e.target.value || undefined })}>
                      <option value="">Escolha…</option>
                      {(queues.data ?? []).map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
                    </select>
                  </Field>
                )}
              </div>
              {canOverride && !ro && (
                <div className="mt-2 flex flex-wrap gap-4">
                  <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={s.allow_closed} onChange={(e) => patch(i, { allow_closed: e.target.checked })} /> Entregar mesmo com o destino fechado</label>
                  <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={s.allow_off_shift} onChange={(e) => patch(i, { allow_off_shift: e.target.checked })} /> Permitir atendente fora da escala</label>
                </div>
              )}
              {(s.allow_closed || s.allow_off_shift) && <p className="mt-2 text-xs text-text-tertiary">Contingência: {s.allow_closed ? 'entrega mesmo com o destino fechado' : ''}{s.allow_closed && s.allow_off_shift ? '; ' : ''}{s.allow_off_shift ? 'aceita atendente fora da escala' : ''}. Alterar isto exige a permissão de contingência.</p>}
            </Card>
          </li>
        ))}
      </ol>
      {!ro && <Button variant="secondary" size="sm" disabled={draft.steps.length >= draft.max_steps} onClick={addStep}>+ Etapa</Button>}
      {problems.length > 0 && !ro && draft.steps.length > 0 && <Notice tone="warning">{problems.join(' ')}</Notice>}
      {!ro && (
        <div className="flex flex-wrap justify-end gap-2">
          {policy.data && <Button variant="secondary" disabled={remove.isPending} onClick={() => setRemoving(true)}>Remover a política</Button>}
          <Button disabled={problems.length > 0 || save.isPending} onClick={() => save.mutate()}>Salvar política</Button>
        </div>
      )}
      <ConfirmDialog open={removing} title="Remover a política de transbordo?" message="As conversas desta fila deixam de ser movidas automaticamente. As que já foram movidas continuam onde estão e o histórico é mantido."
        confirmLabel="Remover" destructive isPending={remove.isPending} onCancel={() => setRemoving(false)} onConfirm={() => { remove.mutate(); setRemoving(false) }} />
    </div>
  )
}

function Situation({ queueId }: { queueId: string }) {
  const tenantId = getTenantId()
  const availability = useQuery({ queryKey: ['att-availability', tenantId, queueId], queryFn: () => calendarsAPI.availability(queueId), retry: false })
  const snapshot = useQuery({ queryKey: ['att-eligibility', tenantId, queueId], queryFn: () => overflowAPI.eligibility(queueId), retry: false })
  if (snapshot.isLoading) return <Skeleton className="h-24" />
  if (snapshot.isError) return <ErrorState message="Não foi possível carregar quem pode atender agora." />
  const s = snapshot.data!
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        {availability.data && (availability.data.open ? <StatusBadge status="success">Aberto agora</StatusBadge> : <StatusBadge status="warning">Fechado agora</StatusBadge>)}
        <StatusBadge status={s.eligible_count > 0 ? 'success' : 'danger'}>{s.eligible_count} {s.eligible_count === 1 ? 'atendente pode' : 'atendentes podem'} receber agora</StatusBadge>
        {s.presence_required && <Badge size="sm">exige presença online</Badge>}
      </div>
      <ul className="divide-y divide-border-subtle text-sm">
        {s.agents.map((a) => (
          <li key={a.user_id} className="flex flex-wrap justify-between gap-2 py-1">
            <span>{a.name || 'Atendente'}</span>
            <span className={a.eligible ? 'text-status-success' : 'text-text-secondary'}>{a.eligible ? `elegível${a.on_call ? ' (de plantão)' : ''} · ${a.workload}/${capacityText(a.capacity)}` : `${a.reasons.map(reasonLabel).join(', ')} · ${a.workload}/${capacityText(a.capacity)}`}</span>
          </li>
        ))}
        {s.agents.length === 0 && <li className="py-1 text-text-secondary">Nenhum atendente nesta fila.</li>}
      </ul>
    </div>
  )
}

const REASONS: Record<string, string> = {
  queue_closed: 'setor fechado', off_shift: 'fora da escala', offline: 'offline', presence_unavailable: 'presença indisponível', no_capacity: 'capacidade esgotada',
  unavailable: 'marcou-se indisponível', inactive: 'inativo', not_member: 'fora da fila',
}
const reasonLabel = (r: string) => REASONS[r] ?? r.replace(/_/g, ' ')

function SLASection({ manage, queueId }: { manage: boolean; queueId: string }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<SLAPolicy | null>(null)
  const policies = useQuery({ queryKey: ['att-sla', tenantId], queryFn: slaAPI.list, retry: false })
  const monitor = useQuery({ queryKey: ['att-sla-monitor', tenantId, queueId], queryFn: () => slaAPI.monitor(queueId), retry: false })
  const sectors = useQuery({ queryKey: ['att-sectors', tenantId], queryFn: sectorsAPI.list, retry: false })
  const queues = useQuery({ queryKey: ['att-queues', tenantId], queryFn: queuesAPI.list, retry: false })
  const scopeName = (p: SLAPolicy) => (p.scope_kind === 'tenant' ? 'Toda a empresa' : p.scope_kind === 'sector' ? `Setor ${sectors.data?.find((s) => s.id === p.scope_id)?.name ?? '?'}` : `Fila ${queues.data?.find((q) => q.id === p.scope_id)?.name ?? '?'}`)
  const save = useMutation({
    mutationFn: (p: SLAPolicy) => slaAPI.put(p, p.revision),
    onSuccess: async () => { setEditing(null); await qc.invalidateQueries({ queryKey: ['att-sla', tenantId] }) },
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const remove = useMutation({
    mutationFn: (p: SLAPolicy) => slaAPI.remove(p.scope_kind, p.scope_id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['att-sla', tenantId] }),
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const metric = (label: string, key: keyof SLAPolicy, e: SLAPolicy) => (
    <Input label={`${label} (minutos)`} type="number" min={0} value={e[key] ? String(Math.round(Number(e[key]) / 60)) : ''} helperText="Vazio = sem meta"
      onChange={(ev) => setEditing({ ...e, [key]: ev.target.value === '' || Number(ev.target.value) <= 0 ? undefined : Number(ev.target.value) * 60 })} />
  )
  const summary = (p: SLAPolicy) => [
    p.first_response_seconds && `1ª resposta ${formatSeconds(p.first_response_seconds)}`,
    p.queue_wait_seconds && `espera na fila ${formatSeconds(p.queue_wait_seconds)}`,
    p.assignment_seconds && `atribuição ${formatSeconds(p.assignment_seconds)}`,
    p.resolution_seconds && `resolução ${formatSeconds(p.resolution_seconds)}`,
  ].filter(Boolean).join(' · ')

  return (
    <section aria-label="Metas de SLA" className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-lg font-semibold text-text-primary">Metas de SLA</h2>
        {manage && <Button size="sm" onClick={() => setEditing({ scope_kind: 'tenant', risk_percent: 80, counting: 'wall', active: true })}>+ Nova meta</Button>}
      </div>
      <Notice>A meta mais específica vence: fila, depois setor, depois empresa. Ao mudar de fila ou setor o tempo já vivido é mantido (o relógio nunca reinicia). Passar do prazo avisa e escala, mas nunca tira a conversa de quem já a atende.</Notice>
      {error && <ErrorState title="Não foi possível concluir" message={error} isDismissible onDismiss={() => setError(null)} />}
      {policies.data?.length === 0 && <EmptyState title="Nenhuma meta de SLA" description="Sem meta, nada é cronometrado." />}
      {policies.data?.map((p) => (
        <Card key={`${p.scope_kind}${p.scope_id ?? ''}`} padding="compact" aria-label={`Meta ${scopeName(p)}`}>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h3 className="text-sm font-semibold text-text-primary">{scopeName(p)} {!p.active && <Badge size="sm">inativa</Badge>}</h3>
              <p className="text-xs text-text-tertiary">{summary(p) || 'sem metas definidas'} · alerta aos {p.risk_percent}% · {p.counting === 'business' ? 'horário útil' : 'tempo corrido'}</p>
            </div>
            {manage && <div className="flex gap-2"><Button size="sm" variant="secondary" onClick={() => setEditing(p)}>Editar</Button><Button size="sm" variant="secondary" disabled={remove.isPending} onClick={() => remove.mutate(p)}>Remover</Button></div>}
          </div>
        </Card>
      ))}
      {monitor.data && (
        <Card padding="compact" aria-label="Monitor de SLA da fila">
          <h3 className="text-sm font-semibold text-text-primary">Relógios da fila selecionada</h3>
          <p className="text-sm text-text-secondary">
            {Object.keys(monitor.data.counts).length === 0 ? 'Nenhum relógio de SLA em andamento.' : Object.entries(monitor.data.counts).map(([k, v]) => `${k === 'at_risk' ? 'em risco' : k === 'breached' ? 'violados' : k === 'running' ? 'no prazo' : k}: ${v}`).join(' · ')}
          </p>
        </Card>
      )}
      <Modal open={!!editing} title="Meta de SLA" onClose={() => setEditing(null)}
        footer={<><Button variant="secondary" size="sm" onClick={() => setEditing(null)}>Cancelar</Button><Button size="sm" disabled={save.isPending || (editing?.scope_kind !== 'tenant' && !editing?.scope_id)} onClick={() => save.mutate(editing!)}>Salvar</Button></>}>
        {editing && (
          <div className="space-y-3">
            <Field label="Vale para">
              <select className={selectClass} value={editing.scope_kind} disabled={editing.revision !== undefined} onChange={(e) => setEditing({ ...editing, scope_kind: e.target.value as SLAScope, scope_id: undefined })}>
                <option value="tenant">Toda a empresa</option><option value="sector">Um setor</option><option value="queue">Uma fila</option>
              </select>
            </Field>
            {editing.scope_kind !== 'tenant' && (
              <Field label={editing.scope_kind === 'sector' ? 'Setor' : 'Fila'}>
                <select className={selectClass} value={editing.scope_id ?? ''} disabled={editing.revision !== undefined} onChange={(e) => setEditing({ ...editing, scope_id: e.target.value || undefined })}>
                  <option value="">Escolha…</option>
                  {(editing.scope_kind === 'sector' ? sectors.data ?? [] : queues.data ?? []).map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
                </select>
              </Field>
            )}
            {metric('Primeira resposta', 'first_response_seconds', editing)}
            {metric('Espera na fila', 'queue_wait_seconds', editing)}
            {metric('Atribuição a um atendente', 'assignment_seconds', editing)}
            {metric('Resolução', 'resolution_seconds', editing)}
            <Input label="Alertar quando chegar a (%) do prazo" type="number" min={1} max={99} value={String(editing.risk_percent)} onChange={(e) => setEditing({ ...editing, risk_percent: Number(e.target.value) || 80 })} />
            <Field label="Contagem">
              <select className={selectClass} value={editing.counting} onChange={(e) => setEditing({ ...editing, counting: e.target.value as SLAPolicy['counting'] })}>
                <option value="wall">Tempo corrido (24 h)</option><option value="business">Só horário útil do setor</option>
              </select>
            </Field>
            <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={editing.active} onChange={(e) => setEditing({ ...editing, active: e.target.checked })} /> Meta ativa</label>
          </div>
        )}
      </Modal>
    </section>
  )
}

export default function AttendanceOverflowPage() {
  const tenantId = getTenantId()
  const canRead = useAccess().can('agent.read')
  const [queueId, setQueueId] = useState('')
  const queues = useQuery({ queryKey: ['att-queues', tenantId], queryFn: queuesAPI.list, retry: false, enabled: canRead })
  useEffect(() => { if (!queueId && queues.data?.length) setQueueId(queues.data[0].id) }, [queues.data, queueId])
  return (
    <AttendanceFrame title="Transbordo e SLA" description="O que acontece com a conversa quando a fila está fechada, sem atendente ou lotada, e quanto tempo cada etapa pode levar.">
      {({ manage }) => (
        <div className="space-y-8">
          {queues.isLoading && <Skeleton className="h-16" />}
          {queues.data?.length === 0 && <EmptyState title="Nenhuma fila" description="Crie filas em Atendimento › Filas e setores para configurar o transbordo." />}
          {queues.data && queues.data.length > 0 && (
            <>
              <Field label="Fila de atendimento">
                <select className={`${selectClass} max-w-sm`} value={queueId} onChange={(e) => setQueueId(e.target.value)}>
                  {queues.data.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
                </select>
              </Field>
              {queueId && (
                <>
                  <section aria-label="Situação agora" className="space-y-2"><h2 className="text-lg font-semibold text-text-primary">Situação agora</h2><Situation queueId={queueId} /></section>
                  <section aria-label="Política de transbordo" className="space-y-2"><h2 className="text-lg font-semibold text-text-primary">Transbordo em cadeia</h2><PolicyEditor key={queueId} queueId={queueId} manage={manage} /></section>
                  <SLASection manage={manage} queueId={queueId} />
                </>
              )}
            </>
          )}
        </div>
      )}
    </AttendanceFrame>
  )
}
