import { useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Input, Modal, Skeleton, StatusBadge, TextArea } from '../components/primitives'
import { AttendanceFrame, Field, Notice, selectClass } from '../components/attendance/AttendanceFrame'
import { queuesAPI } from '../lib/queues'
import { attendanceErrorMessage, calendarsAPI, noticeAPI, sectorsAPI, type Availability, type Sector } from '../lib/sectors'
import { getTenantId } from '../lib/session'

function when(iso: string | undefined, tz?: string): string {
  if (!iso) return ''
  try {
    return new Date(iso).toLocaleString('pt-BR', { weekday: 'short', day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: tz || undefined })
  } catch {
    return new Date(iso).toLocaleString('pt-BR')
  }
}

function OpenBadge({ a }: { a?: Availability }) {
  if (!a) return <Badge size="sm">verificando…</Badge>
  if (a.open) return <StatusBadge status="success" size="sm">Aberto agora{a.next_close ? ` · fecha ${when(a.next_close, a.timezone)}` : ''}</StatusBadge>
  return <StatusBadge status="warning" size="sm">Fechado{a.next_open ? ` · abre ${when(a.next_open, a.timezone)}` : ''}</StatusBadge>
}

export default function AttendanceSectorsPage() {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [renaming, setRenaming] = useState<Sector | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [deactivating, setDeactivating] = useState<Sector | null>(null)
  const [noticeFor, setNoticeFor] = useState<{ id: string; name: string } | null>(null)
  const [noticeText, setNoticeText] = useState('')

  const sectors = useQuery({ queryKey: ['att-sectors', tenantId], queryFn: sectorsAPI.list, retry: false })
  const queues = useQuery({ queryKey: ['att-queues', tenantId], queryFn: queuesAPI.list, retry: false })
  const calendars = useQuery({ queryKey: ['att-calendars', tenantId], queryFn: calendarsAPI.list, retry: false })
  const availability = useQueries({
    queries: (queues.data ?? []).map((q) => ({ queryKey: ['att-availability', tenantId, q.id], queryFn: () => calendarsAPI.availability(q.id), retry: false, staleTime: 30_000 })),
  })
  const availOf = (id: string): Availability | undefined => availability[(queues.data ?? []).findIndex((q) => q.id === id)]?.data

  const refresh = async () => {
    await Promise.all([
      qc.invalidateQueries({ queryKey: ['att-sectors', tenantId] }),
      qc.invalidateQueries({ queryKey: ['att-queues', tenantId] }),
      qc.invalidateQueries({ queryKey: ['att-availability', tenantId] }),
    ])
  }
  const run = useMutation({
    mutationFn: (action: () => Promise<unknown>) => action(),
    onMutate: () => setError(null),
    onSuccess: refresh,
    onError: (err) => { setError(attendanceErrorMessage(err)); void refresh() },
  })

  const assigned = new Set((sectors.data ?? []).flatMap((s) => s.queues.map((q) => q.id)))
  const unassigned = (queues.data ?? []).filter((q) => !assigned.has(q.id))
  const calendarName = (id?: string) => calendars.data?.items.find((c) => c.id === id)?.name

  const openNotice = async (id: string, name: string) => {
    setNoticeFor({ id, name })
    setNoticeText('')
    try { setNoticeText((await noticeAPI.get(id)).text) } catch { /* o formulário abre vazio; salvar mostra o erro */ }
  }

  return (
    <AttendanceFrame
      title="Setores"
      description="Agrupe os grupos de atendimento em setores (Comercial, Financeiro, Suporte…) e defina quando cada um atende."
      actions={undefined}
    >
      {({ manage }) => (
        <div className="space-y-6">
          {manage && (
            <div className="flex justify-end">
              <Button onClick={() => { setNewName(''); setCreating(true) }}>+ Novo setor</Button>
            </div>
          )}
          {error && <ErrorState title="Não foi possível concluir" message={error} isDismissible onDismiss={() => setError(null)} />}
          {sectors.isLoading && <Skeleton className="h-40" />}
          {sectors.isError && <ErrorState message="Não foi possível carregar os setores." action={{ label: 'Tentar novamente', onClick: () => { void sectors.refetch() } }} />}
          {sectors.data?.length === 0 && <EmptyState title="Nenhum setor ainda" description="Os setores padrão (Comercial, Financeiro e Suporte) são criados com a empresa. Crie outros quando precisar." />}

          {sectors.data?.map((s) => (
            <Card key={s.id} padding="compact" aria-label={`Setor ${s.name}`}>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h2 className="text-base font-semibold text-text-primary">{s.name} {!s.active && <Badge size="sm">inativo</Badge>}</h2>
                  <p className="text-xs text-text-tertiary">
                    Horário: {s.calendar_id ? calendarName(s.calendar_id) ?? 'calendário removido' : 'herda o padrão da empresa'}
                  </p>
                </div>
                {manage && (
                  <div className="flex flex-wrap gap-2">
                    <Button size="sm" variant="secondary" onClick={() => { setRenaming(s); setRenameValue(s.name) }}>Renomear</Button>
                    {s.active
                      ? <Button size="sm" variant="secondary" onClick={() => setDeactivating(s)}>Desativar</Button>
                      : <Button size="sm" variant="secondary" disabled={run.isPending} onClick={() => run.mutate(() => sectorsAPI.update(s.id, { active: true }))}>Reativar</Button>}
                  </div>
                )}
              </div>

              {manage && (
                <div className="mt-3 max-w-sm">
                  <Field label="Horário de atendimento do setor" hint="Os grupos do setor herdam este horário, a menos que tenham um próprio.">
                    <select className={selectClass} value={s.calendar_id ?? ''} disabled={run.isPending}
                      onChange={(e) => run.mutate(() => sectorsAPI.update(s.id, { calendar_id: e.target.value || null }))}>
                      <option value="">Herdar o padrão da empresa</option>
                      {calendars.data?.items.map((c) => <option key={c.id} value={c.id}>{c.name} — {c.timezone}</option>)}
                    </select>
                  </Field>
                </div>
              )}

              <ul className="mt-4 divide-y divide-border-subtle" aria-label={`Grupos de ${s.name}`}>
                {s.queues.length === 0 && <li className="py-2 text-sm text-text-secondary">Nenhum grupo neste setor. Sem grupo, o setor não consegue receber conversas.</li>}
                {s.queues.map((q) => (
                  <li key={q.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium text-text-primary">{q.name}</span>
                      <OpenBadge a={availOf(q.id)} />
                    </div>
                    {manage && (
                      <div className="flex flex-wrap gap-2">
                        <Button size="sm" variant="secondary" onClick={() => void openNotice(q.id, q.name)}>Aviso fora do expediente</Button>
                        <Button size="sm" variant="secondary" disabled={run.isPending} onClick={() => run.mutate(() => sectorsAPI.setQueueSector(q.id, null))}>Tirar do setor</Button>
                      </div>
                    )}
                  </li>
                ))}
              </ul>
            </Card>
          ))}

          {unassigned.length > 0 && (
            <Card padding="compact" aria-label="Grupos sem setor">
              <h2 className="text-base font-semibold text-text-primary">Grupos sem setor</h2>
              <p className="text-xs text-text-tertiary">Continuam funcionando como antes; coloque-os em um setor para aplicar horário, escala e transbordo por setor.</p>
              <ul className="mt-3 divide-y divide-border-subtle">
                {unassigned.map((q) => (
                  <li key={q.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium text-text-primary">{q.name}</span>
                      <OpenBadge a={availOf(q.id)} />
                    </div>
                    {manage && sectors.data && sectors.data.length > 0 && (
                      <select aria-label={`Colocar ${q.name} em um setor`} className={`${selectClass} max-w-[220px]`} value="" disabled={run.isPending}
                        onChange={(e) => { const v = e.target.value; if (v) run.mutate(() => sectorsAPI.setQueueSector(q.id, v)) }}>
                        <option value="">Colocar em um setor…</option>
                        {sectors.data.filter((s) => s.active).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                      </select>
                    )}
                  </li>
                ))}
              </ul>
            </Card>
          )}

          <Modal open={creating} title="Novo setor" onClose={() => setCreating(false)}
            footer={<><Button variant="secondary" size="sm" onClick={() => setCreating(false)}>Cancelar</Button>
              <Button size="sm" disabled={!newName.trim() || run.isPending} onClick={() => { run.mutate(() => sectorsAPI.create(newName.trim())); setCreating(false) }}>Criar setor</Button></>}>
            <Input label="Nome do setor" value={newName} maxLength={60} onChange={(e) => setNewName(e.target.value)} />
          </Modal>

          <Modal open={!!renaming} title="Renomear setor" description="Os fluxos continuam funcionando: eles guardam o setor pelo identificador, não pelo nome." onClose={() => setRenaming(null)}
            footer={<><Button variant="secondary" size="sm" onClick={() => setRenaming(null)}>Cancelar</Button>
              <Button size="sm" disabled={!renameValue.trim() || run.isPending} onClick={() => { const id = renaming!.id; run.mutate(() => sectorsAPI.update(id, { name: renameValue.trim() })); setRenaming(null) }}>Salvar</Button></>}>
            <Input label="Nome do setor" value={renameValue} maxLength={60} onChange={(e) => setRenameValue(e.target.value)} />
          </Modal>

          <ConfirmDialog open={!!deactivating} title={`Desativar ${deactivating?.name ?? 'setor'}?`}
            message="O setor deixa de ser oferecido ao cliente e de receber conversas novas. Fluxos que o usam seguem a saída “indisponível”. O histórico e as conversas em andamento não mudam. Você pode reativá-lo depois."
            confirmLabel="Desativar" destructive isPending={run.isPending}
            onCancel={() => setDeactivating(null)} onConfirm={() => { const id = deactivating!.id; run.mutate(() => sectorsAPI.update(id, { active: false })); setDeactivating(null) }} />

          <Modal open={!!noticeFor} title={`Aviso fora do expediente — ${noticeFor?.name ?? ''}`} onClose={() => setNoticeFor(null)}
            footer={<><Button variant="secondary" size="sm" onClick={() => setNoticeFor(null)}>Cancelar</Button>
              <Button size="sm" disabled={run.isPending} onClick={() => { const id = noticeFor!.id; run.mutate(() => noticeAPI.put(id, noticeText)); setNoticeFor(null) }}>Salvar</Button></>}>
            <div className="space-y-3">
              <TextArea label="Mensagem ao cliente" rows={4} maxLength={500} value={noticeText} onChange={(e) => setNoticeText(e.target.value)}
                helperText="Enviada uma vez por atendimento quando o cliente chega com o grupo fechado. Use {{proxima_abertura}} para informar quando volta. Vazio = não envia nada." />
              <Notice>Se um fluxo publicado já avisa o cliente sobre o horário, deixe este campo vazio para não repetir a mensagem.</Notice>
            </div>
          </Modal>

          {calendars.data && <p className="text-xs text-text-tertiary">Fuso padrão para novos horários: {calendars.data.default_timezone}.</p>}
        </div>
      )}
    </AttendanceFrame>
  )
}
