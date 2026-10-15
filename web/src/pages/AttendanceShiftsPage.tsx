import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, Card, EmptyState, ErrorState, Input, Modal, Skeleton, StatusBadge } from '../components/primitives'
import { AttendanceFrame, Field, Notice, selectClass } from '../components/attendance/AttendanceFrame'
import { agentsAPI } from '../lib/agents'
import { attendanceErrorMessage, calendarsAPI, sectorsAPI, shiftsAPI, type AgentShift, type Roster } from '../lib/sectors'
import { queuesAPI } from '../lib/queues'
import { zonedToIso } from '../lib/attendanceModel'
import { getTenantId } from '../lib/session'

const KIND_LABEL: Record<string, string> = { on_call: 'Plantão', contingency: 'Contingência' }

function ShiftsSection({ manage }: { manage: boolean }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [error, setError] = useState<string | null>(null)
  const [draft, setDraft] = useState<Record<string, { calendar: string; scope: string }>>({})
  const agents = useQuery({ queryKey: ['att-agents', tenantId], queryFn: agentsAPI.list, retry: false })
  const shifts = useQuery({ queryKey: ['att-shifts', tenantId], queryFn: () => shiftsAPI.list(), retry: false })
  const calendars = useQuery({ queryKey: ['att-calendars', tenantId], queryFn: calendarsAPI.list, retry: false })
  const sectors = useQuery({ queryKey: ['att-sectors', tenantId], queryFn: sectorsAPI.list, retry: false })
  const queues = useQuery({ queryKey: ['att-queues', tenantId], queryFn: queuesAPI.list, retry: false })

  const calName = (id: string) => calendars.data?.items.find((c) => c.id === id)?.name ?? 'horário removido'
  const scopeName = (s: AgentShift) => (s.queue_id ? `fila ${queues.data?.find((q) => q.id === s.queue_id)?.name ?? '?'}` : s.sector_id ? `setor ${sectors.data?.find((x) => x.id === s.sector_id)?.name ?? '?'}` : 'todos os setores e filas')
  const scopeBody = (scope: string) => (scope.startsWith('q:') ? { queue_id: scope.slice(2) } : scope.startsWith('s:') ? { sector_id: scope.slice(2) } : {})

  const set = useMutation({
    mutationFn: (b: { user_id: string; calendar_id: string | null; scope: string }) => shiftsAPI.set({ user_id: b.user_id, calendar_id: b.calendar_id, ...scopeBody(b.scope) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['att-shifts', tenantId] }),
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const active = (agents.data ?? []).filter((a) => a.status === 'active')

  return (
    <section aria-label="Escalas dos atendentes" className="space-y-3">
      <h2 className="text-lg font-semibold text-text-primary">Escalas dos atendentes</h2>
      <Notice>Quem não tem escala fica disponível 24 horas (padrão). A escala de uma pessoa vale somente dentro do horário do setor: o atendente só recebe conversa quando o setor está aberto <em>e</em> ele está de turno, online e com capacidade.</Notice>
      {error && <ErrorState title="Não foi possível concluir" message={error} isDismissible onDismiss={() => setError(null)} />}
      {agents.isLoading && <Skeleton className="h-32" />}
      {agents.data && active.length === 0 && <EmptyState title="Nenhum atendente ativo" description="Em Pessoas, abra a pessoa e ligue “Recebe conversas” para definir a escala dela." />}
      {active.map((a) => {
        const mine = (shifts.data ?? []).filter((s) => s.user_id === a.user_id)
        const d = draft[a.user_id] ?? { calendar: '', scope: '' }
        return (
          <Card key={a.id} padding="compact" aria-label={`Escala de ${a.name || a.email}`}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="text-sm font-semibold text-text-primary">{a.name || a.email}</h3>
              {mine.length === 0 && <Badge size="sm">24 h (padrão)</Badge>}
            </div>
            <ul className="mt-2 space-y-1">
              {mine.map((s) => (
                <li key={`${s.queue_id ?? ''}${s.sector_id ?? ''}`} className="flex flex-wrap items-center justify-between gap-2 text-sm">
                  <span>{calName(s.calendar_id)} — {scopeName(s)}</span>
                  {manage && <Button size="sm" variant="secondary" disabled={set.isPending} onClick={() => set.mutate({ user_id: a.user_id, calendar_id: null, scope: s.queue_id ? `q:${s.queue_id}` : s.sector_id ? `s:${s.sector_id}` : '' })}>Remover</Button>}
                </li>
              ))}
            </ul>
            {manage && calendars.data && calendars.data.items.length > 0 && (
              <div className="mt-3 flex flex-wrap items-end gap-2">
                <Field label={`Escala para ${a.name || a.email}`}>
                  <select className={selectClass} value={d.calendar} onChange={(e) => setDraft({ ...draft, [a.user_id]: { ...d, calendar: e.target.value } })}>
                    <option value="">Escolha um horário</option>
                    {calendars.data.items.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                  </select>
                </Field>
                <Field label="Vale para">
                  <select aria-label={`Escopo da escala de ${a.name || a.email}`} className={selectClass} value={d.scope} onChange={(e) => setDraft({ ...draft, [a.user_id]: { ...d, scope: e.target.value } })}>
                    <option value="">Todos os setores e filas</option>
                    {sectors.data?.map((s) => <option key={s.id} value={`s:${s.id}`}>Setor {s.name}</option>)}
                    {queues.data?.map((q) => <option key={q.id} value={`q:${q.id}`}>Fila {q.name}</option>)}
                  </select>
                </Field>
                <Button size="sm" disabled={!d.calendar || set.isPending} onClick={() => { set.mutate({ user_id: a.user_id, calendar_id: d.calendar, scope: d.scope }); setDraft({ ...draft, [a.user_id]: { calendar: '', scope: '' } }) }}>Definir escala</Button>
              </div>
            )}
          </Card>
        )
      })}
    </section>
  )
}

function Periods({ roster, manage, tz, onClose }: { roster: Roster; manage: boolean; tz: string; onClose: () => void }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [error, setError] = useState<string | null>(null)
  const [user, setUser] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [capacity, setCapacity] = useState('1')
  const agents = useQuery({ queryKey: ['att-agents', tenantId], queryFn: agentsAPI.list, retry: false })
  const periods = useQuery({ queryKey: ['att-periods', tenantId, roster.id], queryFn: () => shiftsAPI.periods(roster.id), retry: false })
  const nameOf = (id: string) => agents.data?.find((a) => a.user_id === id)?.name || 'pessoa removida'
  const refresh = () => Promise.all([qc.invalidateQueries({ queryKey: ['att-periods', tenantId, roster.id] }), qc.invalidateQueries({ queryKey: ['att-rosters', tenantId] })])
  const add = useMutation({
    mutationFn: () => shiftsAPI.addPeriod(roster.id, { user_id: user, from: zonedToIso(from, tz), to: zonedToIso(to, tz), capacity: Number(capacity) || 1 }),
    onSuccess: () => { setUser(''); setFrom(''); setTo(''); void refresh() },
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const remove = useMutation({
    mutationFn: (id: string) => shiftsAPI.removePeriod(roster.id, id),
    onSuccess: refresh,
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const fmt = (iso: string) => new Date(iso).toLocaleString('pt-BR', { timeZone: tz, day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
  return (
    <Modal open title={`${KIND_LABEL[roster.kind] ?? 'Plantão'}: ${roster.name}`} description={`Períodos dos próximos 30 dias. Horários no fuso ${tz}. Cada período tem no máximo 31 dias.`} onClose={onClose}
      footer={<Button variant="secondary" size="sm" onClick={onClose}>Fechar</Button>}>
      <div className="space-y-4">
        {error && <ErrorState title="Não foi possível concluir" message={error} isDismissible onDismiss={() => setError(null)} />}
        {periods.isLoading && <Skeleton className="h-20" />}
        {periods.data?.length === 0 && <p className="text-sm text-text-secondary">Ninguém escalado nos próximos 30 dias.</p>}
        <ul className="space-y-1">
          {periods.data?.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
              <span>{nameOf(p.user_id)} · {fmt(p.from)} → {fmt(p.to)} · capacidade {p.capacity}</span>
              {manage && <Button size="sm" variant="secondary" disabled={remove.isPending} onClick={() => remove.mutate(p.id)}>Remover</Button>}
            </li>
          ))}
        </ul>
        {manage && (
          <div className="grid gap-2 sm:grid-cols-2">
            <Field label="Atendente">
              <select className={selectClass} value={user} onChange={(e) => setUser(e.target.value)}>
                <option value="">Escolha</option>
                {agents.data?.filter((a) => a.status === 'active').map((a) => <option key={a.id} value={a.user_id}>{a.name || a.email}</option>)}
              </select>
            </Field>
            <Input label="Capacidade durante o plantão" type="number" min={1} max={100} value={capacity} onChange={(e) => setCapacity(e.target.value)} />
            <Input label="De" type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
            <Input label="Até" type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
            <div className="sm:col-span-2 flex justify-end"><Button size="sm" disabled={!user || !from || !to || add.isPending} onClick={() => add.mutate()}>Adicionar período</Button></div>
          </div>
        )}
      </div>
    </Modal>
  )
}

function RostersSection({ manage }: { manage: boolean }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [kind, setKind] = useState('on_call')
  const [sector, setSector] = useState('')
  const [open, setOpen] = useState<Roster | null>(null)
  const rosters = useQuery({ queryKey: ['att-rosters', tenantId], queryFn: shiftsAPI.rosters, retry: false })
  const sectors = useQuery({ queryKey: ['att-sectors', tenantId], queryFn: sectorsAPI.list, retry: false })
  const calendars = useQuery({ queryKey: ['att-calendars', tenantId], queryFn: calendarsAPI.list, retry: false })
  const refresh = () => qc.invalidateQueries({ queryKey: ['att-rosters', tenantId] })
  const create = useMutation({
    mutationFn: () => shiftsAPI.createRoster({ name: name.trim(), kind, sector_id: sector || null }),
    onSuccess: () => { setCreating(false); setName(''); void refresh() },
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const toggle = useMutation({
    mutationFn: (r: Roster) => shiftsAPI.updateRoster(r.id, { active: !r.active }),
    onSuccess: refresh,
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const tz = calendars.data?.default_timezone ?? 'America/Manaus'
  return (
    <section aria-label="Plantões e contingência" className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-lg font-semibold text-text-primary">Plantões e contingência</h2>
        {manage && <Button size="sm" onClick={() => setCreating(true)}>+ Nova lista de plantão</Button>}
      </div>
      <Notice>Uma lista de plantão recebe conversas que o transbordo envia fora do expediente ou quando ninguém do setor pode atender. Quem está de plantão atende mesmo fora da própria escala, só dentro do período marcado.</Notice>
      {error && <ErrorState title="Não foi possível concluir" message={error} isDismissible onDismiss={() => setError(null)} />}
      {rosters.isLoading && <Skeleton className="h-20" />}
      {rosters.data?.length === 0 && <EmptyState title="Nenhuma lista de plantão" description="Crie uma lista, escale pessoas por período e use-a como destino na política de transbordo." />}
      {rosters.data?.map((r) => (
        <Card key={r.id} padding="compact" aria-label={`Plantão ${r.name}`}>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h3 className="text-sm font-semibold text-text-primary">{r.name} <Badge size="sm">{KIND_LABEL[r.kind] ?? r.kind}</Badge> {!r.active && <Badge size="sm">desativada</Badge>}</h3>
              <p className="text-xs text-text-tertiary">{r.sector_id ? `Setor ${sectors.data?.find((s) => s.id === r.sector_id)?.name ?? '?'}` : 'Toda a empresa'}</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {r.active && <StatusBadge status={r.on_now > 0 ? 'success' : 'warning'} size="sm">{r.on_now > 0 ? `${r.on_now} de plantão agora` : 'Ninguém de plantão agora'}</StatusBadge>}
              <Button size="sm" variant="secondary" onClick={() => setOpen(r)}>Períodos</Button>
              {manage && <Button size="sm" variant="secondary" disabled={toggle.isPending} onClick={() => toggle.mutate(r)}>{r.active ? 'Desativar' : 'Reativar'}</Button>}
            </div>
          </div>
        </Card>
      ))}
      <Modal open={creating} title="Nova lista de plantão" onClose={() => setCreating(false)}
        footer={<><Button variant="secondary" size="sm" onClick={() => setCreating(false)}>Cancelar</Button><Button size="sm" disabled={!name.trim() || create.isPending} onClick={() => create.mutate()}>Criar</Button></>}>
        <div className="space-y-3">
          <Input label="Nome" value={name} maxLength={60} onChange={(e) => setName(e.target.value)} />
          <Field label="Tipo">
            <select className={selectClass} value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="on_call">Plantão (escala de sobreaviso)</option>
              <option value="contingency">Contingência (última linha de atendimento)</option>
            </select>
          </Field>
          <Field label="Setor (opcional)">
            <select className={selectClass} value={sector} onChange={(e) => setSector(e.target.value)}>
              <option value="">Toda a empresa</option>
              {sectors.data?.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          </Field>
        </div>
      </Modal>
      {open && <Periods roster={open} manage={manage} tz={tz} onClose={() => setOpen(null)} />}
    </section>
  )
}

export default function AttendanceShiftsPage() {
  return (
    <AttendanceFrame title="Escalas e plantões" description="Quem atende em quais horários, e quem cobre fora do expediente.">
      {({ manage }) => (
        <div className="space-y-10">
          <ShiftsSection manage={manage} />
          <RostersSection manage={manage} />
        </div>
      )}
    </AttendanceFrame>
  )
}
