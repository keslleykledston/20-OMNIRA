import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Input } from '../primitives'
import {
  BUILTIN_RESULTS,
  SESSION_STATUS_LABEL,
  TASK_STATUS_LABEL,
  isModuleAbsent,
  newKey,
  serviceAPI,
  serviceErrorMessage,
  type ServiceSession,
} from '../../lib/serviceContext'
import type { DelegatedTarget } from '../../lib/attendance'
import { getTenantId } from '../../lib/session'
import { getActingHub } from '../../lib/acting'

const selectClass = 'w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary'
const active = (s: ServiceSession) => s.status === 'open' || s.status === 'in_progress' || s.status === 'waiting'
const when = (iso: string) => new Date(iso).toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })

interface Props {
  conversationId: string
  // What the person may do (the server refuses anything else; this only keeps the screen from offering it).
  can: (permission: string) => boolean
  // Set when the conversation is attended through the Hub: the instance and the Hub it is served through.
  hub?: DelegatedTarget
  // Attended through the Hub's own pane (the global interceptor already names the Hub): only separates the cache from the member's.
  delegated?: boolean
  // The conversation itself is closed: a session cannot be opened for it.
  conversationClosed?: boolean
}

// Atendimento com protocolo (ADR-0049). Mostra o protocolo AT-n, o estado, o resultado do encerramento e as tarefas do atendimento.
// Encerrar o ATENDIMENTO não finaliza a conversa (isso continua sendo "Finalizar atendimento"). Se o módulo está desligado (404/501), o painel some.
export default function ServiceSessionPanel({ conversationId, can, hub, delegated = false, conversationClosed = false }: Props) {
  const tenantId = hub?.tenantId ?? getTenantId()
  const qc = useQueryClient()
  // the cache is per viewer's capacity AND per Hub: two Hubs serving the same company must never share what was loaded for the other
  const kind = hub || delegated ? `delegated:${hub?.hubId ?? getActingHub()}` : 'member'
  const sessionKey = ['service-session', kind, tenantId, conversationId]
  const [error, setError] = useState<string | null>(null)
  const [closing, setClosing] = useState(false)
  const [code, setCode] = useState('information_provided')
  const [note, setNote] = useState('')
  const [title, setTitle] = useState('')
  const openKey = useRef(newKey()) // one key per mount: a double click or a retry returns the same session

  const q = useQuery({ queryKey: sessionKey, queryFn: () => serviceAPI.ofConversation(conversationId, hub), enabled: !!tenantId && !!conversationId, retry: false })
  const session = q.data
  const codes = useQuery({ queryKey: ['service-codes', kind, tenantId], queryFn: () => serviceAPI.resultCodes(hub), enabled: closing && !!tenantId, retry: false })
  const tasksKey = ['service-tasks', kind, tenantId, session?.id]
  const tasks = useQuery({ queryKey: tasksKey, queryFn: () => serviceAPI.tasksOfSession(session!.id, hub), enabled: !!session?.id, retry: false })

  const refresh = () => { void qc.invalidateQueries({ queryKey: sessionKey }); void qc.invalidateQueries({ queryKey: ['service-tasks', kind, tenantId] }) }
  const fail = (err: unknown) => { setError(serviceErrorMessage(err)); refresh() } // a stale screen reloads what the server has
  const done = () => { setError(null); refresh() }

  const open = useMutation({ mutationFn: () => serviceAPI.open(conversationId, openKey.current, hub), onSuccess: () => { openKey.current = newKey(); done() }, onError: fail })
  const setStatus = useMutation({ mutationFn: (s: 'in_progress' | 'waiting') => serviceAPI.setStatus(session!, s, hub), onSuccess: done, onError: fail })
  const close = useMutation({
    mutationFn: () => serviceAPI.close(session!, { resolution_code: code, note: note.trim() || undefined }, hub),
    onSuccess: () => { setClosing(false); setNote(''); done() },
    onError: fail,
  })
  const addTask = useMutation({
    mutationFn: () => serviceAPI.createTask({ title: title.trim(), session_id: session!.id }, newKey(), hub),
    onSuccess: () => { setTitle(''); done() },
    onError: fail,
  })
  const complete = useMutation({ mutationFn: (t: Parameters<typeof serviceAPI.completeTask>[0]) => serviceAPI.completeTask(t, hub), onSuccess: done, onError: fail })

  // Module off, or no right to read: nothing to show (the rest of the pane is unaffected).
  if (q.isError && isModuleAbsent(q.error) && !can('service.create')) return null
  if (q.isLoading) return null
  if (q.isError && !isModuleAbsent(q.error)) return null

  const busy = open.isPending || setStatus.isPending || close.isPending || addTask.isPending || complete.isPending

  if (!session) {
    if (!can('service.create') || conversationClosed) return null
    return (
      <section aria-label="Atendimento com protocolo" className="px-4 pb-4 text-sm">
        <p className="mb-2 text-text-secondary">Esta conversa ainda não tem um atendimento com protocolo.</p>
        <Button variant="secondary" size="sm" disabled={busy} onClick={() => open.mutate()}>Abrir atendimento com protocolo</Button>
        {error && <p role="alert" className="mt-2 rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">{error}</p>}
      </section>
    )
  }

  const isActive = active(session)
  const pending = (tasks.data ?? []).filter((t) => t.status === 'open' || t.status === 'in_progress')
  const finished = (tasks.data ?? []).filter((t) => t.status === 'done' || t.status === 'cancelled')

  return (
    <section aria-label="Atendimento com protocolo" className="space-y-3 px-4 pb-4 text-sm">
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[13px]">
        <dt className="text-text-secondary">Protocolo</dt>
        <dd className="text-right font-semibold text-text-primary" data-testid="service-protocol">{session.protocol}</dd>
        <dt className="text-text-secondary">Situação</dt>
        <dd className="text-right text-text-primary">{SESSION_STATUS_LABEL[session.status]}</dd>
        <dt className="text-text-secondary">Aberto em</dt>
        <dd className="text-right text-text-primary">{when(session.opened_at)}</dd>
        {session.closed_at && (<><dt className="text-text-secondary">Encerrado em</dt><dd className="text-right text-text-primary">{when(session.closed_at)}</dd></>)}
        {session.resolution_canonical && (<><dt className="text-text-secondary">Resultado</dt><dd className="text-right text-text-primary">{BUILTIN_RESULTS.find((r) => r.code === session.resolution_canonical)?.label ?? 'Abandonado'}</dd></>)}
      </dl>

      {isActive && can('service.update') && (
        <div className="flex flex-wrap gap-2">
          {session.status !== 'waiting' && <Button variant="secondary" size="sm" disabled={busy} onClick={() => setStatus.mutate('waiting')}>Marcar como aguardando</Button>}
          {session.status !== 'in_progress' && <Button variant="secondary" size="sm" disabled={busy} onClick={() => setStatus.mutate('in_progress')}>Marcar em andamento</Button>}
        </div>
      )}

      {isActive && can('service.close') && !closing && (
        <Button variant="secondary" size="sm" disabled={busy} onClick={() => setClosing(true)}>Encerrar atendimento com protocolo</Button>
      )}
      {isActive && closing && (
        <div className="space-y-2 rounded-control border border-border-subtle bg-surface p-3">
          <label className="block text-xs text-text-secondary" htmlFor="svc-result">Resultado</label>
          <select id="svc-result" className={selectClass} value={code} onChange={(e) => setCode(e.target.value)}>
            {BUILTIN_RESULTS.map((r) => <option key={r.code} value={r.code}>{r.label}</option>)}
            {(codes.data ?? []).filter((c) => !BUILTIN_RESULTS.some((b) => b.code === c.code)).map((c) => <option key={c.id} value={c.code}>{c.label}</option>)}
          </select>
          <Input label="Observação (opcional)" value={note} maxLength={1000} onChange={(e) => setNote(e.target.value)} />
          <p className="m-0 text-xs text-text-tertiary">A conversa continua aberta. Para encerrá-la, use “Finalizar atendimento”.</p>
          <div className="flex gap-2">
            <Button size="sm" disabled={busy} onClick={() => close.mutate()}>Confirmar encerramento</Button>
            <Button variant="secondary" size="sm" disabled={busy} onClick={() => setClosing(false)}>Cancelar</Button>
          </div>
        </div>
      )}

      <div>
        <h4 className="mb-1 text-xs font-semibold uppercase text-text-tertiary">Tarefas do atendimento</h4>
        {(tasks.data ?? []).length === 0 && <p className="m-0 text-xs text-text-tertiary">Nenhuma tarefa.</p>}
        <ul className="m-0 list-none space-y-1 p-0">
          {pending.map((t) => (
            <li key={t.id} className="flex items-center justify-between gap-2 rounded-control border border-border-subtle bg-surface p-2 text-xs">
              <span className={t.overdue ? 'text-status-danger' : 'text-text-primary'}>{t.title}{t.overdue ? ' (atrasada)' : ''}</span>
              {can('service.close') && <button type="button" className="text-accent-primary" disabled={busy} aria-label={`Concluir tarefa: ${t.title}`} onClick={() => complete.mutate(t)}>Concluir</button>}
            </li>
          ))}
          {finished.map((t) => (
            <li key={t.id} className="rounded-control border border-border-subtle p-2 text-xs text-text-tertiary">{t.title} · {TASK_STATUS_LABEL[t.status]}</li>
          ))}
        </ul>
        {can('service.create') && (
          <form className="mt-2 flex gap-2" onSubmit={(e) => { e.preventDefault(); if (title.trim()) addTask.mutate() }}>
            <Input aria-label="Nova tarefa" placeholder="Nova tarefa" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />
            <Button type="submit" size="sm" variant="secondary" disabled={busy || !title.trim()}>Adicionar</Button>
          </form>
        )}
      </div>
      {error && <p role="alert" className="rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">{error}</p>}
    </section>
  )
}
