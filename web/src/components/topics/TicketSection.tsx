import React, { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getTenantId } from '../../lib/session'
import { ticketActionLabel, topicsAPI } from '../../lib/topics'
import { Chip, SectionTitle, SmallButton, describeError, isOff } from './shared'

// Ticket context of the subject. The policy advises; nothing here opens a second concurrent ticket in a conversation
// (the backend refuses it) and every action is a person's click.
export function TicketSection({ topicId, canManage }: { topicId: string; canManage: boolean }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const tickets = useQuery({ queryKey: ['topic-tickets', tenantId, topicId], queryFn: () => topicsAPI.tickets(topicId), retry: false })
  const advice = useQuery({ queryKey: ['topic-ticket-advice', tenantId, topicId], queryFn: () => topicsAPI.ticketAdvice(topicId), retry: false })
  const apply = useMutation({
    mutationFn: (a: 'adopt_active' | 'share_active' | 'create') => topicsAPI.applyTicketAction(topicId, a),
    onSuccess: () => {
      setError('')
      qc.invalidateQueries({ queryKey: ['topic-tickets', tenantId, topicId] })
      qc.invalidateQueries({ queryKey: ['topic-ticket-advice', tenantId, topicId] })
    },
    onError: (e) => setError(describeError(e, 'Não foi possível vincular o chamado.')),
  })
  const list = tickets.data ?? []
  const off = isOff(advice.error)
  return (
    <section aria-label="Chamados do assunto">
      <SectionTitle>Chamados do assunto</SectionTitle>
      {list.length === 0 && !tickets.isLoading && <p className="text-xs text-text-secondary">Nenhum chamado vinculado a este assunto.</p>}
      <ul className="space-y-1.5">
        {list.map((t) => (
          <li key={t.id} className="flex items-center justify-between gap-2 rounded-control bg-surface-muted px-2 py-1.5 text-xs">
            <span className="min-w-0 truncate text-text-primary">{t.subject || 'Chamado sem assunto'}</span>
            <span className="flex flex-shrink-0 items-center gap-1.5">
              <Chip tone={t.relation === 'primary' ? 'info' : 'neutral'}>{t.relation === 'primary' ? 'principal' : t.relation === 'merged' ? 'unido' : 'relacionado'}</Chip>
              <Chip tone="neutral">{t.status}</Chip>
            </span>
          </li>
        ))}
      </ul>
      {!off && advice.data && advice.data.action !== 'none' && (
        <div className="mt-2 space-y-2 rounded-control border border-border-subtle p-2">
          <p className="text-xs text-text-secondary">{advice.data.reason}</p>
          {canManage && advice.data.allowed_actions.length > 0 && (
            <div className="flex flex-wrap gap-2">
              {advice.data.allowed_actions.map((a) => (
                <SmallButton key={a} disabled={apply.isPending} onClick={() => apply.mutate(a)}>
                  {ticketActionLabel[a]}
                </SmallButton>
              ))}
            </div>
          )}
          {advice.data.action === 'needs_agent' && advice.data.allowed_actions.length === 0 && (
            <p className="text-[11px] text-text-tertiary">Decisão do atendente: use o painel de chamados da conversa.</p>
          )}
        </div>
      )}
      {error && (
        <p role="alert" className="mt-2 text-xs text-status-danger">
          {error}
        </p>
      )}
    </section>
  )
}
