import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button, ErrorState, LoadingState, Modal } from '../primitives'
import { queuesAPI } from '../../lib/queues'
import { getTenantId } from '../../lib/session'

// Send the conversation to another queue (a department: Comercial, Vendas, Suporte...). The queue's own rules then distribute it among its people. If someone holds the
// conversation (you), they are released: it leaves your list and the history records it. The queue it is already in is shown but cannot be picked.
export default function QueueSelectDialog({ open, currentQueueId, busy, error, onSend, onClose }: {
  open: boolean
  currentQueueId?: string | null
  busy: boolean
  error: string | null
  onSend: (queueId: string, queueName: string) => void
  onClose: () => void
}) {
  const tenantId = getTenantId()
  const [picked, setPicked] = useState('')
  const queues = useQuery({ queryKey: ['queues', tenantId], queryFn: queuesAPI.list, enabled: open, retry: false })
  const list = queues.data ?? []
  const chosen = list.find((q) => q.id === picked)
  return (
    <Modal open={open} title="Enviar para uma fila" description="Escolha o setor que vai continuar este atendimento. As regras da fila escolhem quem atende; a conversa sai da sua lista e fica registrada no histórico." onClose={onClose}
      footer={<>
        <Button variant="secondary" size="sm" onClick={onClose} disabled={busy}>Cancelar</Button>
        <Button size="sm" onClick={() => chosen && onSend(chosen.id, chosen.name)} disabled={!chosen || busy} isLoading={busy}>Enviar</Button>
      </>}>
      {error && <ErrorState title="Não foi possível enviar" message={error} />}
      {queues.isLoading && <LoadingState message="Buscando as filas…" />}
      {queues.isError && <ErrorState title="Não foi possível listar as filas" message="Tente novamente em instantes." />}
      {queues.isSuccess && list.length === 0 && <p className="text-sm text-text-secondary">Esta empresa ainda não tem filas.</p>}
      <fieldset className="space-y-1" aria-label="Filas">
        {list.map((q) => {
          const current = q.id === currentQueueId
          return (
            <label key={q.id} className={`flex items-center gap-2 rounded-control px-2 py-1.5 text-sm ${current ? 'text-text-tertiary' : 'text-text-primary hover:bg-surface-muted'}`}>
              <input type="radio" name="queue" value={q.id} disabled={current || busy} checked={picked === q.id} onChange={() => setPicked(q.id)} />
              <span className="min-w-0 flex-1 truncate">{q.name}</span>
              {current && <span className="text-xs">fila atual</span>}
              {!current && q.mode !== 'manual' && <span className="text-xs text-text-tertiary">{q.available_count} disponíveis</span>}
            </label>
          )
        })}
      </fieldset>
    </Modal>
  )
}
