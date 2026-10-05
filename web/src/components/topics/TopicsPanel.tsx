import React, { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import clsx from 'clsx'
import { getTenantId } from '../../lib/session'
import { useAccess } from '../../lib/useAccess'
import { Ambiguity, Topic, topicsAPI } from '../../lib/topics'
import { Chip, SectionTitle, SmallButton, describeError, formatWhen, isOff, plural } from './shared'
import { SummarySection } from './SummarySection'
import { TimelineSection } from './TimelineSection'
import { TicketSection } from './TicketSection'
import { CopilotSection } from './CopilotSection'
import { HandoffSection } from './HandoffSection'

const statusLabel: Record<string, string> = { open: 'Aberto', resolved: 'Resolvido', archived: 'Arquivado' }

// ADR-0017: the conversation is the physical thread; a TOPIC is the subject being handled. This panel sits beside the
// conversation (never replaces it): the subjects, what is waiting for a person's decision, and for the selected subject
// its summary, logical timeline, tickets, a reply draft and a private-chat invite.
export default function TopicsPanel({ conversationId }: { conversationId: string }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const { can } = useAccess()
  const canManage = can('topic.manage')
  const [selected, setSelected] = useState<string | null>(null)
  const [newTitle, setNewTitle] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    setSelected(null)
    setError('')
    setNewTitle('')
  }, [conversationId])

  const topicsKey = ['conversation-topics', tenantId, conversationId]
  const ambKey = ['conversation-ambiguities', tenantId, conversationId]
  const topics = useQuery({ queryKey: topicsKey, queryFn: () => topicsAPI.listForConversation(conversationId), retry: false })
  const ambiguities = useQuery({ queryKey: ambKey, queryFn: () => topicsAPI.ambiguities(conversationId), retry: false })
  const refreshAll = () => {
    qc.invalidateQueries({ queryKey: topicsKey })
    qc.invalidateQueries({ queryKey: ambKey })
  }

  const create = useMutation({
    mutationFn: (title: string) => topicsAPI.create(conversationId, title),
    onSuccess: (t) => {
      setNewTitle('')
      setError('')
      setSelected(t.id)
      refreshAll()
    },
    onError: (e) => setError(describeError(e, 'Não foi possível criar o assunto.')),
  })
  const resolve = useMutation({
    mutationFn: (v: { id: string; choice: { topic_id: string } | { new_topic_title: string } }) => topicsAPI.resolveAmbiguity(v.id, v.choice),
    onSuccess: () => {
      setError('')
      refreshAll()
      qc.invalidateQueries({ queryKey: ['topic-messages', tenantId] })
    },
    onError: (e) => setError(describeError(e, 'Não foi possível resolver.')),
  })
  const setStatus = useMutation({
    mutationFn: (v: { id: string; status: 'open' | 'resolved' }) => topicsAPI.setStatus(v.id, v.status),
    onSuccess: refreshAll,
    onError: (e) => setError(describeError(e, 'Não foi possível alterar o assunto.')),
  })

  // Topics switched off (or not visible to this user): the panel does not exist, the conversation works as before.
  if (topics.isError && (isOff(topics.error) || (topics.error as any)?.status === 403)) return null
  if (topics.isLoading) return null

  const list: Topic[] = topics.data ?? []
  const waiting: Ambiguity[] = ambiguities.data ?? []
  const current = list.find((t) => t.id === selected) ?? null

  return (
    <div className="p-4 border-b border-border-subtle" aria-label="Assuntos da conversa">
      <h4 className="text-xs font-semibold text-text-tertiary mb-3 uppercase">Assuntos</h4>

      {waiting.length > 0 && (
        <div role="region" aria-label="Aguardando decisão" className="mb-3 space-y-2 rounded-control border border-status-warning p-2">
          <SectionTitle>Mensagens sem assunto definido ({waiting.length})</SectionTitle>
          {waiting.slice(0, 3).map((a) => (
            <div key={a.id} className="space-y-1.5">
              <p className="text-xs text-text-secondary">Em qual assunto esta mensagem entra?</p>
              <div className="flex flex-wrap gap-1.5">
                {a.candidates.map((c) => (
                  <SmallButton key={c.topic_id} disabled={!canManage || resolve.isPending} onClick={() => resolve.mutate({ id: a.id, choice: { topic_id: c.topic_id } })}>
                    {c.title || list.find((t) => t.id === c.topic_id)?.title || 'Assunto'}
                  </SmallButton>
                ))}
                {canManage && (
                  <SmallButton
                    tone="primary"
                    disabled={resolve.isPending}
                    onClick={() => resolve.mutate({ id: a.id, choice: { new_topic_title: newTitle.trim() || 'Novo assunto' } })}
                  >
                    Novo assunto
                  </SmallButton>
                )}
              </div>
            </div>
          ))}
          {waiting.length > 3 && <p className="text-[11px] text-text-tertiary">E mais {waiting.length - 3} aguardando.</p>}
        </div>
      )}

      {list.length === 0 && waiting.length === 0 && <p className="text-xs text-text-secondary mb-2">Nenhum assunto ainda. Crie um para organizar esta conversa.</p>}

      <ul className="space-y-1.5" aria-label="Lista de assuntos">
        {list.map((t) => (
          <li key={t.id}>
            <button
              type="button"
              onClick={() => setSelected(selected === t.id ? null : t.id)}
              aria-pressed={selected === t.id}
              className={clsx(
                'w-full text-left rounded-control border px-2.5 py-2 transition-colors',
                selected === t.id ? 'border-accent-primary bg-accent-primary-soft' : 'border-border-subtle bg-surface hover:bg-surface-muted'
              )}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="min-w-0 truncate text-sm font-medium text-text-primary">{t.title}</span>
                <Chip tone={t.status === 'open' ? 'info' : 'neutral'}>{statusLabel[t.status] ?? t.status}</Chip>
              </div>
              <div className="mt-0.5 flex items-center gap-2 text-[11px] text-text-tertiary">
                <span>{plural(t.message_count ?? 0, 'mensagem', 'mensagens')}</span>
                {(t.ticket_count ?? 0) > 0 && <span>· {plural(t.ticket_count ?? 0, 'chamado', 'chamados')}</span>}
                <span>· {formatWhen(t.last_activity_at)}</span>
              </div>
            </button>
          </li>
        ))}
      </ul>

      {canManage && (
        <form
          className="mt-2 flex gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (newTitle.trim()) create.mutate(newTitle.trim())
          }}
        >
          <input
            aria-label="Título do novo assunto"
            className="min-w-0 flex-1 rounded-control border border-border-subtle bg-surface px-2 py-1.5 text-xs text-text-primary"
            placeholder="Novo assunto (ex.: Pedido 837)"
            maxLength={200}
            value={newTitle}
            onChange={(e) => setNewTitle(e.target.value)}
          />
          <SmallButton disabled={create.isPending || !newTitle.trim()} onClick={() => newTitle.trim() && create.mutate(newTitle.trim())}>
            Criar
          </SmallButton>
        </form>
      )}
      {error && (
        <p role="alert" className="mt-2 text-xs text-status-danger">
          {error}
        </p>
      )}

      {current && (
        <div className="mt-4 space-y-4 border-t border-border-subtle pt-3" aria-label={`Detalhes do assunto ${current.title}`}>
          <div className="flex items-start justify-between gap-2">
            <h5 className="text-sm font-semibold text-text-primary break-words">{current.title}</h5>
            {canManage && (
              <SmallButton disabled={setStatus.isPending} onClick={() => setStatus.mutate({ id: current.id, status: current.status === 'open' ? 'resolved' : 'open' })}>
                {current.status === 'open' ? 'Resolver' : 'Reabrir'}
              </SmallButton>
            )}
          </div>
          <SummarySection topicId={current.id} canManage={canManage} />
          <TimelineSection topicId={current.id} />
          <TicketSection topicId={current.id} canManage={canManage} />
          {canManage && <CopilotSection topicId={current.id} />}
          {canManage && <HandoffSection topicId={current.id} />}
        </div>
      )}
    </div>
  )
}
