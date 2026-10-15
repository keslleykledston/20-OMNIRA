import { useState, type ReactNode } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Input, Modal, Skeleton, TextArea } from '../components/primitives'
import { AttendanceFrame, Field, Notice, selectClass } from '../components/attendance/AttendanceFrame'
import { LineEntryRules } from '../components/people/LineEntryRules'
import { QueueItem } from '../components/queues/QueueItem'
import { NewQueueDialog, type NewQueueValues } from '../components/queues/NewQueueDialog'
import { QueuePeopleDialog } from '../components/queues/QueuePeopleDialog'
import { buildQueueRows, hasNoDefaultQueue, type QueueRow } from '../lib/peopleModel'
import { queueErrorMessage, queuesAPI, type QueueMode } from '../lib/queues'
import { attendanceErrorMessage, calendarsAPI, noticeAPI, openNoticeAPI, sectorsAPI, type Availability, type Sector } from '../lib/sectors'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'

export default function AttendanceSectorsPage() {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const access = useAccess()
  const canRead = access.can('agent.read') // no request is sent before the permission is known (the API refuses anyway)
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [renaming, setRenaming] = useState<Sector | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [deactivating, setDeactivating] = useState<Sector | null>(null)
  const [noticeFor, setNoticeFor] = useState<{ id: string; name: string } | null>(null)
  const [noticeText, setNoticeText] = useState('')
  const [openText, setOpenText] = useState('')
  const [saved, setSaved] = useState({ closed: '', open: '' }) // what the server had: only a field that changed is sent (a field that failed to load is never overwritten by the empty form)
  const [newQueue, setNewQueue] = useState(false)
  const [renamingQueue, setRenamingQueue] = useState<QueueRow | null>(null)
  const [queueName, setQueueName] = useState('')
  const [deletingQueue, setDeletingQueue] = useState<QueueRow | null>(null)
  const [peopleOf, setPeopleOf] = useState<QueueRow | null>(null)

  const sectors = useQuery({ queryKey: ['att-sectors', tenantId], queryFn: sectorsAPI.list, retry: false, enabled: canRead })
  const queues = useQuery({ queryKey: ['att-queues', tenantId], queryFn: queuesAPI.list, retry: false, enabled: canRead })
  const calendars = useQuery({ queryKey: ['att-calendars', tenantId], queryFn: calendarsAPI.list, retry: false, enabled: canRead })
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
  // Queue changes answer with the queue's own refusals (default queue, line still pointing at it, least-loaded without the engine).
  const runQueue = useMutation({
    mutationFn: (action: () => Promise<unknown>) => action(),
    onMutate: () => setError(null),
    onSuccess: refresh,
    onError: (err) => { setError(queueErrorMessage(err)); void refresh() },
  })
  const busy = run.isPending || runQueue.isPending

  const rows = buildQueueRows(queues.data ?? [])
  const rowOf = (id: string) => rows.find((r) => r.id === id)
  const assigned = new Set((sectors.data ?? []).flatMap((s) => s.queues.map((q) => q.id)))
  const unassigned = rows.filter((q) => !assigned.has(q.id))
  const calendarName = (id?: string) => calendars.data?.items.find((c) => c.id === id)?.name

  const openNotice = async (id: string, name: string) => {
    setNoticeFor({ id, name })
    setNoticeText('')
    setOpenText('')
    setSaved({ closed: '', open: '' })
    const [closed, open] = await Promise.allSettled([noticeAPI.get(id), openNoticeAPI.get(id)]) // o formulário abre vazio se falhar; salvar mostra o erro
    const c = closed.status === 'fulfilled' ? closed.value.text : ''
    const o = open.status === 'fulfilled' ? open.value.text : ''
    setNoticeText(c)
    setOpenText(o)
    setSaved({ closed: c, open: o })
  }
  const saveNotices = () => {
    const id = noticeFor!.id
    run.mutate(async () => {
      if (noticeText !== saved.closed) await noticeAPI.put(id, noticeText)
      if (openText !== saved.open) await openNoticeAPI.put(id, openText)
    })
    setNoticeFor(null)
  }
  const createQueue = (v: NewQueueValues) => {
    runQueue.mutate(async () => {
      const created = await queuesAPI.create({ name: v.name, mode: v.mode, is_default: v.isDefault })
      if (v.sectorId && created?.id) await sectorsAPI.setQueueSector(created.id, v.sectorId)
    })
    setNewQueue(false)
  }

  const queueItem = (q: QueueRow, manage: boolean, sectorControl?: ReactNode) => (
    <QueueItem
      key={q.id}
      queue={q}
      availability={availOf(q.id)}
      manage={manage}
      busy={busy}
      sectorControl={sectorControl}
      onPeople={() => setPeopleOf(q)}
      onNotices={() => void openNotice(q.id, q.name)}
      onRename={() => { setQueueName(q.name); setRenamingQueue(q) }}
      onMode={(mode: QueueMode) => runQueue.mutate(() => queuesAPI.update(q.id, { mode }))}
      onMakeDefault={() => runQueue.mutate(() => queuesAPI.update(q.id, { is_default: true }))}
      onDelete={() => setDeletingQueue(q)}
    />
  )

  return (
    <AttendanceFrame
      title="Filas e setores"
      description="A fila é onde as conversas esperam e de onde são distribuídas. Agrupe as filas em setores (Comercial, Financeiro, Suporte…) e defina quando cada um atende."
      actions={undefined}
    >
      {({ manage }) => (
        <div className="space-y-6">
          {manage && (
            <div className="flex flex-wrap justify-end gap-2">
              <Button variant="secondary" onClick={() => setNewQueue(true)}>+ Nova fila</Button>
              <Button onClick={() => { setNewName(''); setCreating(true) }}>+ Novo setor</Button>
            </div>
          )}
          {hasNoDefaultQueue(rows) && (
            <div role="status" className="rounded-card border border-status-warning-border bg-status-warning-soft p-4 text-sm font-medium text-text-primary">
              Nenhuma fila é a padrão: as conversas novas sem regra de canal não estão sendo distribuídas. Abra “Mais ações” de uma fila e escolha “Definir como fila padrão”.
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
                      : <Button size="sm" variant="secondary" disabled={busy} onClick={() => run.mutate(() => sectorsAPI.update(s.id, { active: true }))}>Reativar</Button>}
                  </div>
                )}
              </div>

              {manage && (
                <div className="mt-3 max-w-sm">
                  <Field label="Horário de atendimento do setor" hint="As filas do setor herdam este horário, a menos que tenham um próprio.">
                    <select className={selectClass} value={s.calendar_id ?? ''} disabled={busy}
                      onChange={(e) => run.mutate(() => sectorsAPI.update(s.id, { calendar_id: e.target.value || null }))}>
                      <option value="">Herdar o padrão da empresa</option>
                      {calendars.data?.items.map((c) => <option key={c.id} value={c.id}>{c.name} — {c.timezone}</option>)}
                    </select>
                  </Field>
                </div>
              )}

              <ul className="mt-4 divide-y divide-border-subtle" aria-label={`Filas de ${s.name}`}>
                {s.queues.length === 0 && <li className="py-2 text-sm text-text-secondary">Nenhuma fila neste setor. Sem fila, o setor não consegue receber conversas.</li>}
                {s.queues.map((sq) => {
                  const q = rowOf(sq.id) ?? { id: sq.id, name: sq.name, mode: 'manual' as const, isDefault: false, memberCount: 0, availableCount: 0, openConversationCount: 0 }
                  return queueItem(q, manage, manage ? (
                    <Button size="sm" variant="tertiary" disabled={busy} onClick={() => run.mutate(() => sectorsAPI.setQueueSector(q.id, null))}>Tirar do setor</Button>
                  ) : undefined)
                })}
              </ul>
            </Card>
          ))}

          {unassigned.length > 0 && (
            <Card padding="compact" aria-label="Filas sem setor">
              <h2 className="text-base font-semibold text-text-primary">Filas sem setor</h2>
              <p className="text-xs text-text-tertiary">Continuam funcionando; coloque-as em um setor para aplicar horário, escala e transbordo por setor.</p>
              <ul className="mt-3 divide-y divide-border-subtle" aria-label="Filas sem setor">
                {unassigned.map((q) => queueItem(q, manage, manage && sectors.data && sectors.data.length > 0 ? (
                  <select aria-label={`Colocar ${q.name} em um setor`} className={`${selectClass} max-w-[220px]`} value="" disabled={busy}
                    onChange={(e) => { const v = e.target.value; if (v) run.mutate(() => sectorsAPI.setQueueSector(q.id, v)) }}>
                    <option value="">Colocar em um setor…</option>
                    {sectors.data.filter((s) => s.active).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                  </select>
                ) : undefined))}
              </ul>
            </Card>
          )}

          {queues.data && queues.data.length === 0 && (
            <EmptyState title="Nenhuma fila ainda" description={manage ? 'Crie a primeira fila para começar a distribuir conversas.' : 'Um administrador cria as filas.'} />
          )}

          {canRead && <LineEntryRules groups={rows} canManage={manage && access.can('channel.manage')} />}

          <Modal open={creating} title="Novo setor" onClose={() => setCreating(false)}
            footer={<><Button variant="secondary" size="sm" onClick={() => setCreating(false)}>Cancelar</Button>
              <Button size="sm" disabled={!newName.trim() || busy} onClick={() => { run.mutate(() => sectorsAPI.create(newName.trim())); setCreating(false) }}>Criar setor</Button></>}>
            <Input label="Nome do setor" value={newName} maxLength={60} onChange={(e) => setNewName(e.target.value)} />
          </Modal>

          <Modal open={!!renaming} title="Renomear setor" description="Os fluxos continuam funcionando: eles guardam o setor pelo identificador, não pelo nome." onClose={() => setRenaming(null)}
            footer={<><Button variant="secondary" size="sm" onClick={() => setRenaming(null)}>Cancelar</Button>
              <Button size="sm" disabled={!renameValue.trim() || busy} onClick={() => { const id = renaming!.id; run.mutate(() => sectorsAPI.update(id, { name: renameValue.trim() })); setRenaming(null) }}>Salvar</Button></>}>
            <Input label="Nome do setor" value={renameValue} maxLength={60} onChange={(e) => setRenameValue(e.target.value)} />
          </Modal>

          <ConfirmDialog open={!!deactivating} title={`Desativar ${deactivating?.name ?? 'setor'}?`}
            message="O setor deixa de ser oferecido ao cliente e de receber conversas novas. Fluxos que o usam seguem a saída “indisponível”. O histórico e as conversas em andamento não mudam. Você pode reativá-lo depois."
            confirmLabel="Desativar" destructive isPending={run.isPending}
            onCancel={() => setDeactivating(null)} onConfirm={() => { const id = deactivating!.id; run.mutate(() => sectorsAPI.update(id, { active: false })); setDeactivating(null) }} />

          <Modal open={!!noticeFor} title={`Avisos de horário — ${noticeFor?.name ?? ''}`} onClose={() => setNoticeFor(null)}
            footer={<><Button variant="secondary" size="sm" onClick={() => setNoticeFor(null)}>Cancelar</Button>
              <Button size="sm" disabled={busy} onClick={saveNotices}>Salvar</Button></>}>
            <div className="space-y-3">
              <TextArea label="Quando a fila está fechada" rows={3} maxLength={500} value={noticeText} onChange={(e) => setNoticeText(e.target.value)}
                helperText="Enviada uma vez por atendimento quando o cliente chega com a fila fechada. Use {{proxima_abertura}} para informar quando volta. Vazio = não envia nada." />
              <TextArea label="Quando a fila abrir" rows={3} maxLength={500} value={openText} onChange={(e) => setOpenText(e.target.value)}
                helperText="Enviada uma vez, na hora em que a fila abre, ao cliente que ficou esperando. Ex.: “Já estamos atendendo. Um atendente responderá em instantes.” Vazio = não envia nada." />
              <Notice>Se um fluxo publicado já avisa o cliente sobre o horário, deixe o campo vazio para não repetir a mensagem.</Notice>
            </div>
          </Modal>

          {newQueue && <NewQueueDialog sectors={sectors.data ?? []} busy={runQueue.isPending} onCancel={() => setNewQueue(false)} onCreate={createQueue} />}

          <Modal open={!!renamingQueue} title="Renomear fila" description="Os fluxos continuam funcionando: eles guardam a fila pelo identificador, não pelo nome." onClose={() => setRenamingQueue(null)}
            footer={<><Button variant="secondary" size="sm" onClick={() => setRenamingQueue(null)}>Cancelar</Button>
              <Button size="sm" disabled={!queueName.trim() || busy} onClick={() => { const id = renamingQueue!.id; runQueue.mutate(() => queuesAPI.update(id, { name: queueName.trim() })); setRenamingQueue(null) }}>Salvar</Button></>}>
            <Input label="Nome da fila" value={queueName} maxLength={60} onChange={(e) => setQueueName(e.target.value)} />
          </Modal>

          <ConfirmDialog open={!!deletingQueue} title={`Excluir a fila ${deletingQueue?.name ?? ''}?`}
            message="As pessoas deixam de fazer parte dela. Esta ação não pode ser desfeita."
            confirmLabel="Excluir" destructive isPending={runQueue.isPending}
            onCancel={() => setDeletingQueue(null)} onConfirm={() => { const id = deletingQueue!.id; runQueue.mutate(() => queuesAPI.remove(id)); setDeletingQueue(null) }} />

          {peopleOf && <QueuePeopleDialog queue={rowOf(peopleOf.id) ?? peopleOf} manage={manage} onClose={() => setPeopleOf(null)} />}

          {calendars.data && <p className="text-xs text-text-tertiary">Fuso padrão para novos horários: {calendars.data.default_timezone}.</p>}
        </div>
      )}
    </AttendanceFrame>
  )
}
