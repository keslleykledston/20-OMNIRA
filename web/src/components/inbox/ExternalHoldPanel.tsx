import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import { Button, Modal, TextArea } from '../primitives'
import { API_BASE } from '../../lib/config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session'
import { emitInboxNotice } from '../../lib/inboxNotice'
import { useAccess } from '../../lib/useAccess'

// ADR-0046: a conversation an external application (CRM) holds. The platform shows the KIND of holder only (never an operator of the CRM) and the origin of
// the last change. A supervisor with conversation.takeover_external may take it back: explicit, with a written reason and a confirmation — never automatic.

export const HOLDER_LABEL: Record<string, string> = {
  tenant_user: 'Workspace',
  hub_user: 'Hub',
  crm_application: 'CRM',
  crm_agent_verified: 'CRM',
}

export function isHeldExternally(kind?: string): boolean {
  return kind === 'crm_application' || kind === 'crm_agent_verified'
}

const REASON_MIN = 5

export function takeoverErrorMessage(err: unknown): string {
  const status = (err as { response?: { status?: number } })?.response?.status
  if (status === 403) return 'Você não tem permissão para retomar uma conversa atendida por um CRM.'
  if (status === 409) return 'A conversa mudou enquanto você decidia (talvez o CRM já a devolveu). Atualize e veja o estado atual.'
  if (status === 400) return 'Informe o motivo e confirme.'
  return 'Não foi possível retomar agora. Tente novamente.'
}

function TakeoverDialog({ conversationId, open, onClose }: { conversationId: string; open: boolean; onClose: () => void }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [reason, setReason] = useState('')
  const [confirm, setConfirm] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const takeover = useMutation({
    mutationFn: () => axios.post(`${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/takeover`, { reason: reason.trim(), confirm: true }, { headers: authHeaders() }),
    onSuccess: () => {
      for (const key of ['inbox-conversation-detail', 'inbox-context', 'inbox-messages']) void qc.invalidateQueries({ queryKey: [key, tenantId, conversationId] })
      void qc.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] })
      emitInboxNotice(conversationId, 'Você retomou o atendimento. O CRM não responde mais por esta conversa.')
      setReason('')
      setConfirm(false)
      setError(null)
      onClose()
    },
    onError: (err) => {
      if (isUnauthorized(err)) handleUnauthorized()
      else setError(takeoverErrorMessage(err))
    },
  })
  const valid = reason.trim().length >= REASON_MIN && confirm
  return (
    <Modal
      open={open}
      title="Retomar o atendimento do CRM"
      description="O CRM deixa de poder responder esta conversa e ela passa a ser sua."
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" size="sm" onClick={onClose} disabled={takeover.isPending}>Cancelar</Button>
          <Button size="sm" variant="danger" isLoading={takeover.isPending} disabled={!valid} onClick={() => takeover.mutate()}>Retomar atendimento</Button>
        </>
      }
    >
      <div className="space-y-3">
        {error && <p role="alert" className="rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">{error}</p>}
        <TextArea label="Motivo (fica registrado)" value={reason} rows={3} maxLength={300} onChange={(e) => setReason(e.target.value)} helperText={`Mínimo de ${REASON_MIN} caracteres.`} />
        <label className="flex items-start gap-2 text-sm text-text-primary">
          <input type="checkbox" className="mt-1" checked={confirm} onChange={() => setConfirm(!confirm)} />
          Entendo que o CRM deixa de atender esta conversa e que mensagens que ele já enviou não são desfeitas.
        </label>
      </div>
    </Modal>
  )
}

// Shown in place of the composer while an external application holds the conversation.
export default function ExternalHoldPanel({ conversationId, onRefresh }: { conversationId: string; onRefresh: () => void }) {
  const { can } = useAccess()
  const [open, setOpen] = useState(false)
  return (
    <div role="status" className="space-y-2 border-t border-border-subtle bg-surface-muted px-4 py-3 text-xs text-text-secondary">
      <p>
        Esta conversa está sendo atendida por uma <strong>aplicação externa (CRM)</strong>. A resposta fica desativada aqui para ninguém falar por cima do CRM.
      </p>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="secondary" onClick={onRefresh}>Atualizar conversa</Button>
        {can('conversation.takeover_external') && <Button size="sm" variant="danger" onClick={() => setOpen(true)}>Retomar atendimento</Button>}
      </div>
      <TakeoverDialog conversationId={conversationId} open={open} onClose={() => setOpen(false)} />
    </div>
  )
}
