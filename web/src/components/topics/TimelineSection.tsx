import React from 'react'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import { getTenantId } from '../../lib/session'
import { topicsAPI } from '../../lib/topics'
import { Chip, SectionTitle, formatWhen } from './shared'

const sourceLabel: Record<string, string> = {
  agent: 'por atendente',
  customer: 'pelo cliente',
  handoff: 'convite privado',
  reply: 'resposta direta',
  explicit: 'escolha explícita',
  entity: 'mesmo pedido/documento',
  rule: 'regra',
  ai: 'IA',
  legacy: 'histórico',
}

// The LOGICAL timeline: only the messages of this topic, oldest first. The physical conversation stays untouched in the
// thread on the left, so the attendant never loses the original.
export function TimelineSection({ topicId }: { topicId: string }) {
  const tenantId = getTenantId()
  const q = useQuery({ queryKey: ['topic-messages', tenantId, topicId], queryFn: () => topicsAPI.messages(topicId), retry: false })
  const items = [...(q.data ?? [])].reverse()
  return (
    <section aria-label="Linha do tempo do assunto">
      <SectionTitle>Linha do tempo do assunto</SectionTitle>
      {q.isLoading && <p className="text-xs text-text-tertiary">Carregando…</p>}
      {q.isError && <p className="text-xs text-text-tertiary">Não foi possível carregar as mensagens deste assunto.</p>}
      {!q.isLoading && !q.isError && items.length === 0 && <p className="text-xs text-text-secondary">Nenhuma mensagem vinculada a este assunto.</p>}
      <ol className="space-y-1.5">
        {items.map((m) => (
          <li key={m.id} className={clsx('rounded-control px-2 py-1.5 text-xs', m.direction === 'outbound' ? 'bg-accent-primary-soft' : 'bg-surface-muted')}>
            <div className="mb-0.5 flex flex-wrap items-center gap-1.5 text-[11px] text-text-tertiary">
              <span>{m.direction === 'outbound' ? 'Atendente' : 'Cliente'}</span>
              <span>{formatWhen(m.created_at)}</span>
              {m.relation !== 'primary' && <Chip tone="neutral">{m.relation === 'secondary' ? 'também aqui' : 'apoio'}</Chip>}
              {m.decision_source !== 'agent' && sourceLabel[m.decision_source] && <span>· {sourceLabel[m.decision_source]}</span>}
            </div>
            <p className="text-text-primary whitespace-pre-wrap break-words">{m.body || `(${m.message_type})`}</p>
          </li>
        ))}
      </ol>
    </section>
  )
}
