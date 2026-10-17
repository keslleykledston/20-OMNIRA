import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ActionIcon, Button, Icon, Input } from '../primitives'
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
import { serviceSessionKey } from './ProtocolTag'
import SessionExtras from './SessionExtras'

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
// O protocolo encerra JUNTO com o atendimento, em "Finalizar" (uma ação só, decisão do dono em 2026-10-17): este painel não tem botão de encerrar. Se o módulo está desligado (404/501), o painel some.
export default function ServiceSessionPanel({ conversationId, can, hub, delegated = false, conversationClosed = false }: Props) {
  const tenantId = hub?.tenantId ?? getTenantId()
  const qc = useQueryClient()
  const sessionKey = [...serviceSessionKey(conversationId, hub, delegated)]
  const kind = sessionKey[1] as string // the codes and tasks caches are keyed the same way (per viewer's capacity and per Hub)
  const [error, setError] = useState<string | null>(null)
  const [title, setTitle] = useState('')
  const [adding, setAdding] = useState(false) // the task field is opened on demand, not always on screen
  const openKey = useRef(newKey()) // one key per mount: a double click or a retry returns the same session

  const q = useQuery({ queryKey: sessionKey, queryFn: () => serviceAPI.ofConversation(conversationId, hub), enabled: !!tenantId && !!conversationId, retry: false })
  const session = q.data
  const tasksKey = ['service-tasks', kind, tenantId, session?.id]
  const tasks = useQuery({ queryKey: tasksKey, queryFn: () => serviceAPI.tasksOfSession(session!.id, hub), enabled: !!session?.id, retry: false })

  const refresh = () => { void qc.invalidateQueries({ queryKey: sessionKey }); void qc.invalidateQueries({ queryKey: ['service-tasks', kind, tenantId] }) }
  const fail = (err: unknown) => { setError(serviceErrorMessage(err)); refresh() } // a stale screen reloads what the server has
  const done = () => { setError(null); refresh() }

  const open = useMutation({ mutationFn: () => serviceAPI.open(conversationId, openKey.current, hub), onSuccess: () => { openKey.current = newKey(); done() }, onError: fail })
  const setStatus = useMutation({ mutationFn: (s: 'in_progress' | 'waiting') => serviceAPI.setStatus(session!, s, hub), onSuccess: done, onError: fail })
  const addTask = useMutation({
    mutationFn: () => serviceAPI.createTask({ title: title.trim(), session_id: session!.id }, newKey(), hub),
    onSuccess: () => { setTitle(''); setAdding(false); done() },
    onError: fail,
  })
  const complete = useMutation({ mutationFn: (t: Parameters<typeof serviceAPI.completeTask>[0]) => serviceAPI.completeTask(t, hub), onSuccess: done, onError: fail })

  // Module off, or no right to read: nothing to show (the rest of the pane is unaffected).
  if (q.isError && isModuleAbsent(q.error) && !can('service.create')) return null
  if (q.isLoading) return null
  if (q.isError && !isModuleAbsent(q.error)) return null

  const busy = open.isPending || setStatus.isPending || addTask.isPending || complete.isPending

  if (!session) {
    if (!can('service.create') || conversationClosed) return null
    return (
      <section aria-label="Atendimento com protocolo" className="px-4 pb-3 text-sm">
        <div className="flex items-center justify-between gap-2">
          <p className="m-0 text-xs text-text-secondary">Esta conversa ainda não tem um atendimento com protocolo.</p>
          <ActionIcon icon="plus" label="Abrir atendimento com protocolo" disabled={busy} onClick={() => open.mutate()} />
        </div>
        {error && <p role="alert" className="rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">{error}</p>}
      </section>
    )
  }

  const isActive = active(session)
  const pending = (tasks.data ?? []).filter((t) => t.status === 'open' || t.status === 'in_progress')
  const finished = (tasks.data ?? []).filter((t) => t.status === 'done' || t.status === 'cancelled')

  return (
    <section aria-label="Atendimento com protocolo" className="space-y-2 px-4 pb-3 text-sm">
      <div className="flex items-center justify-between gap-2">
        <p className="m-0 flex min-w-0 items-center gap-2">
          <span className="rounded-control bg-accent-primary-soft px-2 py-0.5 font-mono text-sm font-semibold text-accent-primary" data-testid="service-protocol">{session.protocol}</span>
          {!(isActive && can('service.update')) && <span className="rounded-pill bg-surface px-2 py-0.5 text-xs font-medium text-text-secondary">{SESSION_STATUS_LABEL[session.status]}</span>}
        </p>
        {/* one small selector instead of two bare icons: it says the current situation and what it can become (UX audit A2) */}
        {isActive && can('service.update') && (
          <select
            aria-label="Situação do protocolo"
            disabled={busy}
            value={session.status}
            onChange={(e) => { const v = e.target.value; if (v === 'in_progress' || v === 'waiting') setStatus.mutate(v) }}
            className="h-8 min-w-0 flex-shrink rounded-control border border-border-subtle bg-surface px-2 text-xs text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary disabled:opacity-60"
          >
            {session.status === 'open' && <option value="open" disabled>{SESSION_STATUS_LABEL.open}</option>}
            <option value="in_progress">{SESSION_STATUS_LABEL.in_progress}</option>
            <option value="waiting">{SESSION_STATUS_LABEL.waiting}</option>
          </select>
        )}
      </div>
      <p className="m-0 text-xs text-text-tertiary">
        Aberto {when(session.opened_at)}
        {session.closed_at && ` · encerrado ${when(session.closed_at)}`}
        {session.resolution_canonical && ` · ${BUILTIN_RESULTS.find((r) => r.code === session.resolution_canonical)?.label ?? 'Abandonado'}`}
      </p>

      {isActive && <p className="m-0 text-xs text-text-tertiary">O protocolo encerra junto com o atendimento, em “Finalizar”.</p>}

      <div>
        <h4 className="mb-1 text-xs font-semibold uppercase text-text-tertiary">Tarefas{(tasks.data ?? []).length > 0 ? ` · ${pending.length} pendente${pending.length === 1 ? '' : 's'}` : ''}</h4>
        {(tasks.data ?? []).length === 0 && <p className="m-0 text-xs text-text-tertiary">Nenhuma tarefa.</p>}
        <ul className="m-0 list-none space-y-1 p-0">
          {pending.map((t) => (
            <li key={t.id} className="flex items-center justify-between gap-2 rounded-control border border-border-subtle bg-surface py-1 pl-2 pr-1 text-xs">
              <span className={t.overdue ? 'text-status-danger' : 'text-text-primary'}>{t.title}{t.overdue ? ' (atrasada)' : ''}</span>
              {can('service.close') && <ActionIcon icon="check" label={`Concluir tarefa: ${t.title}`} className="h-6 w-6" disabled={busy} onClick={() => complete.mutate(t)} />}
            </li>
          ))}
          {finished.map((t) => (
            <li key={t.id} className="rounded-control border border-border-subtle px-2 py-1 text-xs text-text-tertiary">{t.title} · {TASK_STATUS_LABEL[t.status]}</li>
          ))}
        </ul>
        {can('service.create') && (adding ? (
          <form className="mt-2 flex items-center gap-2" onSubmit={(e) => { e.preventDefault(); if (title.trim()) addTask.mutate() }}>
            <Input aria-label="Nova tarefa" placeholder="Nova tarefa" value={title} maxLength={200} autoFocus onChange={(e) => setTitle(e.target.value)} onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); setAdding(false); setTitle('') } }} />
            <ActionIcon type="submit" icon="plus" label="Salvar tarefa" disabled={busy || !title.trim()} />
            <ActionIcon icon="close" label="Cancelar tarefa" onClick={() => { setAdding(false); setTitle('') }} />
          </form>
        ) : (
          <Button size="sm" variant="tertiary" className="mt-1" onClick={() => setAdding(true)} disabled={busy}>
            <Icon name="plus" size={16} />
            Adicionar tarefa
          </Button>
        ))}
      </div>
      <SessionExtras session={session} can={can} hub={hub} scope={`${kind}:${tenantId}`} />
      {error && <p role="alert" className="rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">{error}</p>}
    </section>
  )
}
