import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, Card, ConfirmDialog, Drawer, EmptyState, ErrorState, Input, Skeleton } from '../components/primitives'
import { AttendanceFrame, Field, Notice, selectClass } from '../components/attendance/AttendanceFrame'
import { attendanceErrorMessage, calendarsAPI, type Calendar, type CalendarSpec, type CalendarSummary, type Interval, type Suspension, type Weekday } from '../lib/sectors'
import { KIND_LABELS, TIMEZONES, WEEKDAYS, calendarProblems, emptyCalendar, setDayIntervals, zonedToIso } from '../lib/attendanceModel'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'

function IntervalRows({ label, intervals, disabled, onChange }: { label: string; intervals: Interval[]; disabled?: boolean; onChange: (v: Interval[]) => void }) {
  return (
    <div className="space-y-2">
      {intervals.map((iv, i) => (
        <div key={i} className="flex flex-wrap items-end gap-2">
          <Input label={`${label}: início ${i + 1}`} type="time" value={iv.start} disabled={disabled} onChange={(e) => onChange(intervals.map((x, k) => (k === i ? { ...x, start: e.target.value } : x)))} />
          <Input label={`${label}: fim ${i + 1}`} type="time" value={iv.end === '24:00' ? '23:59' : iv.end} disabled={disabled} onChange={(e) => onChange(intervals.map((x, k) => (k === i ? { ...x, end: e.target.value } : x)))} />
          {!disabled && <Button size="sm" variant="secondary" onClick={() => onChange(intervals.filter((_, k) => k !== i))}>Remover faixa</Button>}
        </div>
      ))}
      {!disabled && <Button size="sm" variant="secondary" onClick={() => onChange([...intervals, { start: '08:00', end: '18:00' }])}>+ Faixa</Button>}
    </div>
  )
}

function Editor({ calendar, canManage, onClose, onSaved }: { calendar: Calendar | null; canManage: boolean; onClose: () => void; onSaved: () => void }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [name, setName] = useState(calendar?.name ?? '')
  const [spec, setSpec] = useState<CalendarSpec>(calendar?.definition ?? emptyCalendar('business'))
  const [error, setError] = useState<string | null>(null)
  const [susFrom, setSusFrom] = useState('')
  const [susTo, setSusTo] = useState('')
  const [susReason, setSusReason] = useState('')
  const [suspensions, setSuspensions] = useState<Suspension[]>(calendar?.suspensions ?? [])
  const problems = calendarProblems(name, spec)
  const ro = !canManage

  const save = useMutation({
    mutationFn: () => (calendar ? calendarsAPI.update(calendar.id, name.trim(), spec, calendar.revision) : calendarsAPI.create(name.trim(), spec)),
    onSuccess: async () => { await qc.invalidateQueries({ queryKey: ['att-calendars', tenantId] }); onSaved() },
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const addSus = useMutation({
    mutationFn: () => calendarsAPI.addSuspension(calendar!.id, { from: zonedToIso(susFrom, spec.timezone), to: zonedToIso(susTo, spec.timezone), reason: susReason.trim() || undefined }),
    onSuccess: (s) => { setSuspensions((x) => [...x, s]); setSusFrom(''); setSusTo(''); setSusReason(''); void qc.invalidateQueries({ queryKey: ['att-availability', tenantId] }) },
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const delSus = useMutation({
    mutationFn: (id: string) => calendarsAPI.removeSuspension(calendar!.id, id),
    onSuccess: (_v, id) => { setSuspensions((x) => x.filter((s) => s.id !== id)); void qc.invalidateQueries({ queryKey: ['att-availability', tenantId] }) },
    onError: (e) => setError(attendanceErrorMessage(e)),
  })

  const setKind = (kind: CalendarSpec['kind']) => setSpec((s) => ({ ...emptyCalendar(kind, s.timezone), ...(kind === 'custom' ? { weekly: s.weekly ?? emptyCalendar('custom').weekly } : {}), holidays: s.holidays, exceptions: s.exceptions }))
  const day = (k: Weekday) => spec.weekly?.find((d) => d.day === k)?.intervals ?? []

  return (
    <div className="space-y-5">
      {error && <ErrorState title="Não foi possível concluir" message={error} isDismissible onDismiss={() => setError(null)} />}
      <Input label="Nome do horário" value={name} maxLength={60} disabled={ro} onChange={(e) => setName(e.target.value)} />
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Tipo">
          <select className={selectClass} value={spec.kind} disabled={ro} onChange={(e) => setKind(e.target.value as CalendarSpec['kind'])}>
            {Object.entries(KIND_LABELS).map(([k, l]) => <option key={k} value={k}>{l}</option>)}
          </select>
        </Field>
        <Field label="Fuso horário" hint="Horário de verão é aplicado automaticamente pelo fuso.">
          <select className={selectClass} value={spec.timezone} disabled={ro} onChange={(e) => setSpec({ ...spec, timezone: e.target.value })}>
            {TIMEZONES.some((t) => t.id === spec.timezone) ? null : <option value={spec.timezone}>{spec.timezone}</option>}
            {TIMEZONES.map((t) => <option key={t.id} value={t.id}>{t.label}</option>)}
          </select>
        </Field>
      </div>

      {spec.kind !== '24x7' && (
        <fieldset className="space-y-3">
          <legend className="text-sm font-semibold text-text-primary">Dias e faixas de atendimento</legend>
          <p className="text-xs text-text-tertiary">Dia sem faixa = fechado. Uma faixa que termina antes de começar atravessa a meia-noite (ex.: 22:00 às 06:00 vale para o dia em que começa).</p>
          {WEEKDAYS.map((w) => (
            <div key={w.key} className="rounded-control border border-border-subtle p-3">
              <div className="mb-2 text-sm font-medium text-text-primary">{w.long}</div>
              <IntervalRows label={w.long} intervals={day(w.key)} disabled={ro} onChange={(v) => setSpec(setDayIntervals(spec, w.key, v))} />
            </div>
          ))}
        </fieldset>
      )}

      <fieldset className="space-y-3">
        <legend className="text-sm font-semibold text-text-primary">Feriados</legend>
        <p className="text-xs text-text-tertiary">Cadastro manual por data (sem calendário nacional automático). Sem faixa = fechado o dia todo.</p>
        {(spec.holidays ?? []).map((h, i) => (
          <div key={i} className="rounded-control border border-border-subtle p-3 space-y-2">
            <div className="flex flex-wrap items-end gap-2">
              <Input label={`Data do feriado ${i + 1}`} type="date" value={h.date} disabled={ro} onChange={(e) => setSpec({ ...spec, holidays: spec.holidays!.map((x, k) => (k === i ? { ...x, date: e.target.value } : x)) })} />
              <Input label={`Nome do feriado ${i + 1}`} value={h.name ?? ''} disabled={ro} onChange={(e) => setSpec({ ...spec, holidays: spec.holidays!.map((x, k) => (k === i ? { ...x, name: e.target.value } : x)) })} />
              {!ro && <Button size="sm" variant="secondary" onClick={() => setSpec({ ...spec, holidays: spec.holidays!.filter((_, k) => k !== i) })}>Remover feriado</Button>}
            </div>
            <IntervalRows label={`Feriado ${i + 1}`} intervals={h.intervals ?? []} disabled={ro} onChange={(v) => setSpec({ ...spec, holidays: spec.holidays!.map((x, k) => (k === i ? { ...x, intervals: v } : x)) })} />
          </div>
        ))}
        {!ro && <Button size="sm" variant="secondary" onClick={() => setSpec({ ...spec, holidays: [...(spec.holidays ?? []), { date: '', name: '' }] })}>+ Feriado</Button>}
      </fieldset>

      <fieldset className="space-y-3">
        <legend className="text-sm font-semibold text-text-primary">Exceções por período</legend>
        <p className="text-xs text-text-tertiary">Para recesso, mutirão ou horário especial entre duas datas. A exceção vence o feriado e a semana.</p>
        {(spec.exceptions ?? []).map((x, i) => (
          <div key={i} className="rounded-control border border-border-subtle p-3 space-y-2">
            <div className="flex flex-wrap items-end gap-2">
              <Input label={`Exceção ${i + 1}: de`} type="date" value={x.from} disabled={ro} onChange={(e) => setSpec({ ...spec, exceptions: spec.exceptions!.map((y, k) => (k === i ? { ...y, from: e.target.value } : y)) })} />
              <Input label={`Exceção ${i + 1}: até`} type="date" value={x.to} disabled={ro} onChange={(e) => setSpec({ ...spec, exceptions: spec.exceptions!.map((y, k) => (k === i ? { ...y, to: e.target.value } : y)) })} />
              <Input label={`Nome da exceção ${i + 1}`} value={x.name ?? ''} disabled={ro} onChange={(e) => setSpec({ ...spec, exceptions: spec.exceptions!.map((y, k) => (k === i ? { ...y, name: e.target.value } : y)) })} />
              <label className="flex items-center gap-2 pb-2 text-sm"><input type="checkbox" checked={x.closed} disabled={ro} onChange={(e) => setSpec({ ...spec, exceptions: spec.exceptions!.map((y, k) => (k === i ? { ...y, closed: e.target.checked } : y)) })} /> Fechado no período</label>
              {!ro && <Button size="sm" variant="secondary" onClick={() => setSpec({ ...spec, exceptions: spec.exceptions!.filter((_, k) => k !== i) })}>Remover exceção</Button>}
            </div>
            {!x.closed && <IntervalRows label={`Exceção ${i + 1}`} intervals={x.intervals ?? []} disabled={ro} onChange={(v) => setSpec({ ...spec, exceptions: spec.exceptions!.map((y, k) => (k === i ? { ...y, intervals: v } : y)) })} />}
          </div>
        ))}
        {!ro && <Button size="sm" variant="secondary" onClick={() => setSpec({ ...spec, exceptions: [...(spec.exceptions ?? []), { from: '', to: '', closed: true }] })}>+ Exceção</Button>}
      </fieldset>

      {calendar && (
        <fieldset className="space-y-3">
          <legend className="text-sm font-semibold text-text-primary">Suspensão de emergência</legend>
          <p className="text-xs text-text-tertiary">Fecha o atendimento por um período imediatamente (queda de energia, evento). Vence todas as outras regras. Horas no fuso do horário ({spec.timezone}).</p>
          <ul className="space-y-1">
            {suspensions.map((s) => (
              <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
                <span>{new Date(s.from).toLocaleString('pt-BR', { timeZone: spec.timezone })} → {new Date(s.to).toLocaleString('pt-BR', { timeZone: spec.timezone })}{s.reason ? ` · ${s.reason}` : ''}</span>
                {!ro && <Button size="sm" variant="secondary" disabled={delSus.isPending} onClick={() => delSus.mutate(s.id)}>Encerrar suspensão</Button>}
              </li>
            ))}
          </ul>
          {!ro && (
            <div className="flex flex-wrap items-end gap-2">
              <Input label="Suspender de" type="datetime-local" value={susFrom} onChange={(e) => setSusFrom(e.target.value)} />
              <Input label="Suspender até" type="datetime-local" value={susTo} onChange={(e) => setSusTo(e.target.value)} />
              <Input label="Motivo" value={susReason} maxLength={200} onChange={(e) => setSusReason(e.target.value)} />
              <Button size="sm" disabled={!susFrom || !susTo || addSus.isPending} onClick={() => addSus.mutate()}>Suspender</Button>
            </div>
          )}
        </fieldset>
      )}

      {problems.length > 0 && <Notice tone="warning">{problems.join(' ')}</Notice>}
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>Fechar</Button>
        {!ro && <Button disabled={problems.length > 0 || save.isPending} onClick={() => save.mutate()}>{calendar ? 'Salvar alterações' : 'Criar horário'}</Button>}
      </div>
    </div>
  )
}

export default function AttendanceCalendarsPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<{ calendar: Calendar | null } | null>(null)
  const [removing, setRemoving] = useState<CalendarSummary | null>(null)
  const [error, setError] = useState<string | null>(null)
  const list = useQuery({ queryKey: ['att-calendars', tenantId], queryFn: calendarsAPI.list, retry: false, enabled: access.can('agent.read') })
  const canTenant = access.can('tenant.manage')

  const refresh = () => qc.invalidateQueries({ queryKey: ['att-calendars', tenantId] })
  const remove = useMutation({
    mutationFn: (id: string) => calendarsAPI.remove(id),
    onSuccess: refresh,
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const setDefault = useMutation({
    mutationFn: (id: string | null) => calendarsAPI.setDefault(id),
    onSuccess: refresh,
    onError: (e) => setError(attendanceErrorMessage(e)),
  })
  const open = async (id: string) => {
    try { setEditing({ calendar: await calendarsAPI.get(id) }) } catch (e) { setError(attendanceErrorMessage(e)) }
  }
  useEffect(() => { if (!editing) void refresh() }, [editing]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <AttendanceFrame title="Horários" description="Quando cada setor ou grupo atende: horário comercial, 24 horas ou personalizado, com feriados, exceções e suspensão de emergência.">
      {({ manage }) => (
        <div className="space-y-6">
          {manage && <div className="flex justify-end"><Button onClick={() => setEditing({ calendar: null })}>+ Novo horário</Button></div>}
          {error && <ErrorState title="Não foi possível concluir" message={error} isDismissible onDismiss={() => setError(null)} />}
          {list.isLoading && <Skeleton className="h-32" />}
          {list.isError && <ErrorState message="Não foi possível carregar os horários." action={{ label: 'Tentar novamente', onClick: () => { void list.refetch() } }} />}
          {list.data?.items.length === 0 && <EmptyState title="Nenhum horário cadastrado" description="Sem horário, o atendimento funciona 24 horas, como sempre funcionou. Crie um horário e aplique a um setor para controlar o expediente." />}
          <div className="grid gap-3">
            {list.data?.items.map((c) => (
              <Card key={c.id} padding="compact" aria-label={`Horário ${c.name}`}>
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <h2 className="text-base font-semibold text-text-primary">{c.name} <Badge size="sm">{KIND_LABELS[c.kind]}</Badge></h2>
                    <p className="text-xs text-text-tertiary">{c.timezone} · usado por {c.in_use} {c.in_use === 1 ? 'item' : 'itens'} · alterado em {new Date(c.updated_at).toLocaleDateString('pt-BR')}</p>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button size="sm" variant="secondary" onClick={() => void open(c.id)}>{manage ? 'Editar' : 'Ver'}</Button>
                    {manage && <Button size="sm" variant="secondary" disabled={c.in_use > 0} title={c.in_use > 0 ? 'Em uso: troque o horário dos setores e grupos antes de remover.' : undefined} onClick={() => setRemoving(c)}>Remover</Button>}
                  </div>
                </div>
              </Card>
            ))}
          </div>
          {canTenant && list.data && list.data.items.length > 0 && (
            <Card padding="compact">
              <Field label="Horário padrão da empresa" hint="Vale para os setores e grupos que não têm horário próprio. Sem padrão, esses ficam abertos 24 horas.">
                <select className={`${selectClass} max-w-sm`} value={list.data.default_calendar_id ?? ''} disabled={setDefault.isPending} onChange={(e) => setDefault.mutate(e.target.value || null)}>
                  <option value="">Sem padrão (sempre aberto)</option>
                  {list.data.items.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                </select>
              </Field>
            </Card>
          )}
          <Drawer open={!!editing} title={editing?.calendar ? `Horário: ${editing.calendar.name}` : 'Novo horário'} onClose={() => setEditing(null)}>
            {editing && <Editor key={editing.calendar?.id ?? 'new'} calendar={editing.calendar} canManage={manage} onClose={() => setEditing(null)} onSaved={() => setEditing(null)} />}
          </Drawer>
          <ConfirmDialog open={!!removing} title={`Remover ${removing?.name ?? 'horário'}?`} message="O horário deixa de existir. Isto não afeta conversas já atendidas."
            confirmLabel="Remover" destructive isPending={remove.isPending}
            onCancel={() => setRemoving(null)} onConfirm={() => { const id = removing!.id; remove.mutate(id); setRemoving(null) }} />
        </div>
      )}
    </AttendanceFrame>
  )
}
