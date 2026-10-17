import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ActionIcon, Input } from '../primitives'
import {
  ACTOR_LABEL,
  CASE_STATUS_LABEL,
  EVENT_LABEL,
  REQUEST_STATE_LABEL,
  newKey,
  serviceAPI,
  serviceErrorMessage,
  type ServiceSession,
} from '../../lib/serviceContext'
import type { DelegatedTarget } from '../../lib/attendance'

const when = (iso: string) => new Date(iso).toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
const TITLE = 'mb-1 text-xs font-semibold uppercase text-text-tertiary'

interface Props {
  session: ServiceSession
  can: (permission: string) => boolean
  hub?: DelegatedTarget
  // same cache scope the panel uses (per capacity and per Hub), so two Hubs never share what was loaded for the other
  scope: string
}

// What hangs from a protocol besides its tasks: the occurrences (cases) opened from it, the service orders asked of the ERP and the history of the protocol.
// ERP rule (owner, 2026-10-16): the ERP only receives a record for a task/occurrence or a service order; the protocol alone creates nothing there.
export default function SessionExtras({ session, can, hub, scope }: Props) {
  const qc = useQueryClient()
  const [title, setTitle] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [historyOpen, setHistoryOpen] = useState(false)
  const openKey = useRef(newKey())

  const casesKey = ['service-cases', scope, session.id]
  const cases = useQuery({ queryKey: casesKey, queryFn: () => serviceAPI.casesOfSession(session.id, hub), retry: false })
  const addCase = useMutation({
    mutationFn: () => serviceAPI.openCase({ title: title.trim(), session_id: session.id }, openKey.current, hub),
    onSuccess: () => {
      openKey.current = newKey()
      setTitle('')
      setError(null)
      void qc.invalidateQueries({ queryKey: casesKey })
      void qc.invalidateQueries({ queryKey: ['service-timeline', scope, session.id] })
    },
    onError: (e) => setError(serviceErrorMessage(e)),
  })

  // the requests to the ERP are never delegable: only a member's screen asks, and an empty or refused answer simply shows nothing
  const requests = useQuery({ queryKey: ['service-requests', scope, session.id], queryFn: () => serviceAPI.externalRequestsOfSession(session.id), enabled: !hub, retry: false })
  const history = useQuery({ queryKey: ['service-timeline', scope, session.id], queryFn: () => serviceAPI.timeline(session.id, hub), enabled: historyOpen, retry: false })

  const caseItems = cases.data ?? []
  const requestItems = requests.data ?? []

  return (
    <>
      {(cases.isSuccess || can('service.create')) && (
        <div aria-label="Ocorrências do atendimento">
          <h4 className={TITLE}>Ocorrências</h4>
          {cases.isSuccess && caseItems.length === 0 && <p className="m-0 text-xs text-text-tertiary">Nenhuma ocorrência.</p>}
          <ul className="m-0 list-none space-y-1 p-0">
            {caseItems.map((c) => (
              <li key={c.id} className="flex items-center justify-between gap-2 rounded-control border border-border-subtle bg-surface px-2 py-1 text-xs">
                <span className="min-w-0 truncate text-text-primary"><span className="font-medium">{c.case_number}</span> · {c.title}</span>
                <span className="flex-shrink-0 text-text-tertiary">{CASE_STATUS_LABEL[c.status]}</span>
              </li>
            ))}
          </ul>
          {can('service.create') && (
            <form className="mt-2 flex items-center gap-2" onSubmit={(e) => { e.preventDefault(); if (title.trim()) addCase.mutate() }}>
              <Input aria-label="Nova ocorrência" placeholder="Nova ocorrência" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />
              <ActionIcon type="submit" icon="plus" label="Abrir ocorrência" disabled={addCase.isPending || !title.trim()} />
            </form>
          )}
        </div>
      )}

      {requestItems.length > 0 && (
        <div aria-label="Ordens de serviço no ERP">
          <h4 className={TITLE}>Ordens de serviço no ERP</h4>
          <ul className="m-0 list-none space-y-1 p-0">
            {requestItems.map((r) => (
              <li key={r.id} className="flex items-center justify-between gap-2 rounded-control border border-border-subtle bg-surface px-2 py-1 text-xs">
                <span className="min-w-0 truncate text-text-primary">{r.title}</span>
                <span className={`flex-shrink-0 ${r.state === 'outcome_unknown' ? 'text-status-warning' : 'text-text-tertiary'}`}>{REQUEST_STATE_LABEL[r.state] ?? r.state}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <details onToggle={(e) => setHistoryOpen((e.currentTarget as HTMLDetailsElement).open)}>
        <summary className="cursor-pointer text-xs font-semibold uppercase text-text-tertiary">Histórico do atendimento</summary>
        {history.isLoading && <p className="mt-1 text-xs text-text-tertiary">Carregando…</p>}
        {history.isError && <p className="mt-1 text-xs text-text-tertiary">Não foi possível carregar o histórico.</p>}
        {history.isSuccess && (history.data ?? []).length === 0 && <p className="mt-1 text-xs text-text-tertiary">Sem eventos.</p>}
        <ol className="m-0 mt-1 list-none space-y-1 p-0" aria-label="Histórico do atendimento">
          {(history.data ?? []).map((ev) => (
            <li key={ev.id} className="text-xs text-text-secondary">
              <span className="text-text-primary">{EVENT_LABEL[ev.event_type] ?? ev.event_type}</span>
              <span className="text-text-tertiary"> · {when(ev.occurred_at)} · {ACTOR_LABEL[ev.actor_type] ?? ev.actor_type}</span>
              {ev.note && <span className="block text-text-tertiary">“{ev.note}”</span>}
            </li>
          ))}
        </ol>
      </details>
      {error && <p role="alert" className="rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">{error}</p>}
    </>
  )
}
