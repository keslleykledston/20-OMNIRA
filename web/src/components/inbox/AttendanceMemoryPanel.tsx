import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  attendanceAPI,
  attendanceErrorMessage,
  CLOSE_REASON_LABEL,
  FOLLOW_UP_KIND_LABEL,
  type Closure,
  type FollowUp,
} from '../../lib/attendance'
import { getTenantId } from '../../lib/session'

const when = (iso: string) => new Date(iso).toLocaleDateString('pt-BR', { day: '2-digit', month: '2-digit', year: 'numeric' })
const overdue = (f: FollowUp) => !!f.due_at && new Date(f.due_at).getTime() < Date.now()

// O que aconteceu antes com o contato desta conversa (ADR-0020): atendimentos anteriores com resumo e o que ficou pendente ou
// foi prometido. Só leitura, exceto concluir/descartar uma pendência. `exclude` deixa de fora a própria conversa.
export default function AttendanceMemoryPanel({ conversationId }: { conversationId: string }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [error, setError] = useState<string | null>(null)
  const q = useQuery({
    queryKey: ['attendance-context', tenantId, conversationId],
    queryFn: () => attendanceAPI.contextOf(conversationId),
    enabled: !!tenantId && !!conversationId,
    retry: false,
  })
  const resolve = useMutation({
    mutationFn: (v: { id: string; status: 'done' | 'dropped' }) => attendanceAPI.resolveFollowUp(v.id, { status: v.status }),
    onSuccess: () => { setError(null); void qc.invalidateQueries({ queryKey: ['attendance-context', tenantId] }) },
    onError: (err) => setError(attendanceErrorMessage(err)),
  })

  const history = q.data
  if (!history || (history.attendances.length === 0 && history.open_follow_ups.length === 0)) return null

  return (
    <section aria-label="Histórico do contato" className="space-y-4 border-b border-border-subtle p-4">
      {history.open_follow_ups.length > 0 && (
        <div>
          <h4 className="mb-2 text-xs font-semibold uppercase text-text-tertiary">Pendências do contato</h4>
          <ul className="m-0 list-none space-y-2 p-0">
            {history.open_follow_ups.map((f) => (
              <li key={f.id} className="rounded-control border border-border-subtle bg-surface p-2 text-xs">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-semibold text-text-secondary">{FOLLOW_UP_KIND_LABEL[f.kind]}</span>
                  {f.due_at && <span className={overdue(f) ? 'font-semibold text-status-danger' : 'text-text-tertiary'}>{overdue(f) ? 'Atrasada · ' : 'Até '}{when(f.due_at)}</span>}
                </div>
                <p className="my-1 text-text-primary">{f.text}</p>
                <div className="flex gap-2">
                  <button type="button" className="text-accent-primary" disabled={resolve.isPending} aria-label={`Concluir: ${f.text}`} onClick={() => resolve.mutate({ id: f.id, status: 'done' })}>Concluir</button>
                  <button type="button" className="text-text-secondary" disabled={resolve.isPending} aria-label={`Descartar: ${f.text}`} onClick={() => resolve.mutate({ id: f.id, status: 'dropped' })}>Descartar</button>
                </div>
              </li>
            ))}
          </ul>
          {error && <p role="alert" className="mt-2 rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">{error}</p>}
        </div>
      )}
      {history.attendances.length > 0 && (
        <div>
          <h4 className="mb-2 text-xs font-semibold uppercase text-text-tertiary">Atendimentos anteriores</h4>
          <ul className="m-0 list-none space-y-2 p-0">
            {history.attendances.map((a: Closure) => (
              <li key={a.id} className="rounded-control border border-border-subtle bg-surface p-2 text-xs">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-semibold text-text-secondary">{CLOSE_REASON_LABEL[a.reason]}</span>
                  <span className="text-text-tertiary">{when(a.created_at)}</span>
                </div>
                {a.summary && <p className="my-1 text-text-primary">{a.summary}{a.summary_truth === 'ai_inferred' && <span className="text-text-tertiary"> (rascunho da IA)</span>}</p>}
                {a.tickets_kept > 0 && <p className="m-0 text-text-tertiary">{a.tickets_kept} chamado(s) seguem abertos (ERP ou assunto).</p>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}
