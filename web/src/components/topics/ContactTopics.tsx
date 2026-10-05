import React from 'react'
import { useQuery } from '@tanstack/react-query'
import { getTenantId } from '../../lib/session'
import { topicsAPI } from '../../lib/topics'
import { Chip, SectionTitle, formatWhen, isOff, plural } from './shared'

const statusLabel: Record<string, string> = { open: 'Aberto', resolved: 'Resolvido', archived: 'Arquivado' }

// A contact can have several subjects at once (and a subject can cross conversations and channels). Read-only here;
// the work happens in the conversation. When topics are switched off the section does not exist.
export default function ContactTopics({ contactId }: { contactId: string }) {
  const tenantId = getTenantId()
  const q = useQuery({ queryKey: ['contact-topics', tenantId, contactId], queryFn: () => topicsAPI.listForContact(contactId), retry: false })
  if (q.isLoading) return null
  if (q.isError && (isOff(q.error) || (q.error as any)?.status === 403)) return null
  const items = q.data ?? []
  return (
    <section aria-label="Assuntos do contato" className="rounded-card border border-border-subtle bg-surface p-4">
      <SectionTitle>Assuntos do contato</SectionTitle>
      {q.isError && <p className="text-sm text-text-tertiary">Não foi possível carregar os assuntos.</p>}
      {!q.isError && items.length === 0 && <p className="text-sm text-text-secondary">Este contato ainda não tem assuntos.</p>}
      <ul className="space-y-2">
        {items.map((t) => (
          <li key={t.id} className="flex items-center justify-between gap-3 rounded-control bg-surface-muted px-3 py-2">
            <div className="min-w-0">
              <p className="truncate text-sm font-medium text-text-primary">{t.title}</p>
              <p className="text-xs text-text-tertiary">
                {plural(t.message_count ?? 0, 'mensagem', 'mensagens')} · {plural(t.ticket_count ?? 0, 'chamado', 'chamados')} · {formatWhen(t.last_activity_at)}
              </p>
            </div>
            <Chip tone={t.status === 'open' ? 'info' : 'neutral'}>{statusLabel[t.status] ?? t.status}</Chip>
          </li>
        ))}
      </ul>
    </section>
  )
}
