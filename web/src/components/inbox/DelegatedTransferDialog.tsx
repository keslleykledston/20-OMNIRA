import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import axios from 'axios'
import { API_BASE } from '../../lib/config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session'
import { describeHubWriteError } from '../../lib/hub'
import { Button, ErrorState, LoadingState, Modal } from '../primitives'

interface Candidate {
  user_id: string
  name: string
  email: string
  load: number
}

// Hand the conversation you hold to another person of the Hub while attending an instance through it (ADR-0040 phase 04c). The people offered are what the
// SERVER says may take THIS conversation right now (a live grant that can answer this instance and queue); the choice is proven again, in the transaction,
// for the person picked. The instance's own members are not offered here. Giving the conversation back to the queue needs only the key that claims it.
export default function DelegatedTransferDialog({ conversationId, open, canTransfer, onClose, onDone }: {
  conversationId: string
  open: boolean
  canTransfer: boolean
  onClose: () => void
  onDone: () => void
}) {
  const tenantId = getTenantId()
  const base = `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}`
  const [picked, setPicked] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const candidates = useQuery({
    queryKey: ['delegated-transfer-candidates', tenantId, conversationId],
    enabled: open && canTransfer,
    queryFn: async () => (await axios.get(`${base}/transfer-candidates`, { headers: authHeaders() })).data.items as Candidate[],
    retry: false,
  })
  const run = async (action: 'transfer' | 'unassign') => {
    setBusy(true)
    setError(null)
    try {
      await axios.post(`${base}/${action}`, action === 'transfer' ? { target_user_id: picked } : {}, { headers: authHeaders() })
      setPicked('')
      onDone()
    } catch (err) {
      if (isUnauthorized(err)) handleUnauthorized()
      else setError(describeHubWriteError(err))
      if (canTransfer) void candidates.refetch() // the list may be stale (someone lost access)
    } finally {
      setBusy(false)
    }
  }
  const list = candidates.data ?? []
  return (
    <Modal open={open} title="Transferir conversa" description="Escolha quem vai continuar este atendimento. A conversa sai da sua lista e fica registrada no histórico." onClose={onClose}
      footer={<>
        <Button variant="secondary" size="sm" onClick={() => void run('unassign')} disabled={busy}>Devolver à fila</Button>
        <Button variant="secondary" size="sm" onClick={onClose} disabled={busy}>Cancelar</Button>
        {canTransfer && <Button size="sm" onClick={() => void run('transfer')} disabled={!picked || busy} isLoading={busy}>Transferir</Button>}
      </>}>
      {canTransfer && candidates.isLoading && <LoadingState message="Buscando quem pode receber…" />}
      {canTransfer && candidates.isError && !isUnauthorized(candidates.error) && <ErrorState message={describeHubWriteError(candidates.error)} />}
      {canTransfer && candidates.data && list.length === 0 && <p className="text-sm text-text-secondary">Ninguém mais tem acesso para responder esta instância agora. Você pode devolver a conversa à fila.</p>}
      {!canTransfer && <p className="text-sm text-text-secondary">Seu acesso permite devolver a conversa à fila, mas não escolher quem a recebe.</p>}
      {list.length > 0 && (
        <fieldset>
          <legend className="sr-only">Quem recebe a conversa</legend>
          <ul className="divide-y divide-border-subtle">
            {list.map((c) => (
              <li key={c.user_id}>
                <label className="flex items-center gap-3 py-2 text-sm">
                  <input type="radio" name="delegated-transfer-to" value={c.user_id} checked={picked === c.user_id} onChange={() => setPicked(c.user_id)} aria-label={c.name || c.email} />
                  <span className="font-medium text-text-primary">{c.name || c.email}</span>
                  {c.name && <span className="text-text-secondary">{c.email}</span>}
                  <span className="ml-auto text-xs text-text-tertiary">{c.load} abertas</span>
                </label>
              </li>
            ))}
          </ul>
        </fieldset>
      )}
      {error && <p role="alert" className="mt-2 text-sm text-status-danger">{error}</p>}
    </Modal>
  )
}
