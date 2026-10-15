import { useState } from 'react'
import { Link } from 'react-router-dom'
import clsx from 'clsx'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Modal, Skeleton } from '../primitives'
import { agentsAPI } from '../../lib/agents'
import type { QueueRow } from '../../lib/peopleModel'
import { queueErrorMessage } from '../../lib/queues'
import { getTenantId } from '../../lib/session'
import { Switch } from '../people/controls'

// The same person <-> queue link as the person's sheet, seen from the queue. Only people who already attend can join a queue (the API refuses others).
export function QueuePeopleDialog({ queue, manage, onClose }: { queue: QueueRow; manage: boolean; onClose: () => void }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const agents = useQuery({ queryKey: ['agents', tenantId], queryFn: agentsAPI.list, retry: false })
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const run = async (key: string, action: () => Promise<unknown>) => {
    setBusy(key)
    setError(null)
    try {
      await action()
    } catch (err) {
      setError(queueErrorMessage(err))
    } finally {
      await Promise.all([qc.invalidateQueries({ queryKey: ['agents', tenantId] }), qc.invalidateQueries({ queryKey: ['att-queues', tenantId] })])
      setBusy(null)
    }
  }

  const list = [...(agents.data ?? [])].sort((a, b) => (a.name || a.email).localeCompare(b.name || b.email, 'pt-BR', { sensitivity: 'base' }))
  return (
    <Modal
      open
      title={`Pessoas da fila ${queue.name}`}
      description="Quem recebe as conversas desta fila."
      onClose={onClose}
      footer={<Button size="sm" onClick={onClose}>Concluir</Button>}
    >
      <div className="space-y-3">
        {error && <p role="alert" className="rounded-control border border-status-danger-border bg-status-danger-soft px-3 py-2 text-sm text-status-danger">{error}</p>}
        {agents.isLoading && <Skeleton count={3} height="h-10" />}
        {agents.isError && <p role="alert" className="text-sm text-status-danger">Não foi possível carregar quem atende.</p>}
        {agents.data && list.length === 0 && <p className="text-sm text-text-secondary">Ninguém atende conversas nesta instância ainda.</p>}
        <ul className="space-y-2" aria-label={`Quem atende na fila ${queue.name}`}>
          {list.map((a) => {
            const m = a.queues.find((q) => q.queue_id === queue.id)
            const paused = a.status !== 'active'
            const who = a.name || a.email
            return (
              <li key={a.id} className={clsx('flex flex-wrap items-center justify-between gap-3 rounded-control border p-3', m ? 'border-accent-primary bg-accent-primary-soft' : 'border-border-subtle')}>
                <label className="flex min-w-0 items-center gap-3">
                  <input
                    type="checkbox"
                    checked={!!m}
                    disabled={!manage || paused || busy === a.id}
                    aria-label={`${who} participa da fila ${queue.name}`}
                    onChange={(e) => void run(a.id, () => (e.target.checked ? agentsAPI.addQueue(a.id, queue.id, true, 1) : agentsAPI.removeQueue(a.id, m!.id)))}
                    className="h-4 w-4 accent-[var(--color-accent-primary)]"
                  />
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-medium text-text-primary">{who}</span>
                    {paused && <span className="block text-xs text-text-tertiary">Em pausa: reative em Pessoas para mudar as filas.</span>}
                  </span>
                </label>
                {m && (
                  <span className="inline-flex items-center gap-2 text-sm text-text-secondary">
                    <Switch
                      checked={m.available}
                      onChange={(available) => void run(a.id, () => agentsAPI.updateQueue(a.id, m.id, available, m.capacity))}
                      label={`${who} disponível na fila ${queue.name}`}
                      disabled={!manage || busy === a.id}
                    />
                    {m.available ? 'Disponível' : 'Indisponível'}
                  </span>
                )}
              </li>
            )
          })}
        </ul>
        <p className="text-xs text-text-tertiary">
          Para incluir alguém que ainda não atende, abra a pessoa em <Link to="/settings/people" className="text-accent-primary hover:underline">Pessoas</Link> e ligue “Recebe conversas”.
        </p>
      </div>
    </Modal>
  )
}
