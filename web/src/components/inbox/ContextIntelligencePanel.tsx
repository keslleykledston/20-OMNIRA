import React, { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getTenantId } from '../../lib/session'
import { ContextFact, ContextViewError, contextViewAPI, factKindLabel } from '../../lib/contextView'
import { Chip, SmallButton, formatWhen, plural } from '../topics/shared'

function isOff(err: unknown): boolean {
  return err instanceof ContextViewError && err.status === 404
}

function describeRefreshError(err: unknown): string {
  const status = err instanceof ContextViewError ? err.status : 0
  if (status === 429) return 'A análise já foi solicitada há pouco. Aguarde um instante.'
  if (status === 403) return 'Você não pode atualizar a análise deste atendimento.'
  if (status === 404) return 'Atendimento não encontrado.'
  return 'Não foi possível solicitar a atualização.'
}

const analysisStateLabel: Record<string, string> = {
  none: 'Ainda não analisado',
  ok: 'Analisado',
  empty: 'Nada de novo identificado',
  failed: 'Falha na última análise',
}

function FactRow({ fact }: { fact: ContextFact }) {
  return (
    <li className="rounded-control border border-border-subtle bg-surface px-2.5 py-2">
      <div className="flex items-center justify-between gap-2">
        <Chip tone={fact.kind === 'risk' ? 'danger' : fact.kind === 'suggested_action' ? 'info' : 'neutral'}>{factKindLabel[fact.kind] ?? fact.kind}</Chip>
        {fact.evidence_message_ids.length > 0 && (
          <span className="text-xs text-text-tertiary">{plural(fact.evidence_message_ids.length, 'mensagem citada', 'mensagens citadas')}</span>
        )}
      </div>
      <p className="mt-1 text-sm text-text-primary break-words">{fact.text}</p>
    </li>
  )
}

// CONTEXT.2C: a server-aggregated read model of what CONTEXT.2B's windowed analysis found for this conversation —
// sits beside TopicsPanel (which is per-topic) as the conversation-level overview. Nothing here is sent or applied
// on its own: it is read-only context for a person, same rule the Copilot already follows. The operator
// confirm/dismiss flow for pending_actions is intentionally not built yet (CONTEXT.2D).
export default function ContextIntelligencePanel({ conversationId }: { conversationId: string }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [refreshError, setRefreshError] = useState('')
  const key = ['inbox-context-view', tenantId, conversationId]

  // The analysis itself runs asynchronously in the worker (CONTEXT.2A/2B) — a 202 from refresh only means the
  // window was forced to 'due', not that the new analysis exists yet. Poll while stale so a person who stays on
  // the conversation sees BOTH a manual refresh and the automatic background analysis land, without polling
  // forever once the view is caught up (Codex review round 2).
  const view = useQuery({
    queryKey: key,
    queryFn: () => contextViewAPI.get(conversationId),
    retry: false,
    refetchInterval: (query) => (query.state.data?.stale ? 5_000 : false),
  })

  const refresh = useMutation({
    mutationFn: () => contextViewAPI.refresh(conversationId),
    onSuccess: () => {
      setRefreshError('')
      void qc.invalidateQueries({ queryKey: key })
    },
    onError: (e) => setRefreshError(describeRefreshError(e)),
  })

  if (view.isError && isOff(view.error)) return null // feature off, or this actor cannot see it: the panel simply does not exist
  if (view.isLoading || !view.data) return null

  const v = view.data
  const hasContent = v.topics.length > 0 || v.summary

  return (
    <div className="p-4 border-b border-border-subtle" aria-label="Inteligência de contexto">
      <div className="flex items-center justify-between gap-2 mb-2">
        <h4 className="text-xs font-semibold text-text-tertiary uppercase">Inteligência de contexto</h4>
        <div className="flex items-center gap-1.5">
          {v.stale && <Chip tone="warning">Desatualizado</Chip>}
          <SmallButton disabled={refresh.isPending} onClick={() => refresh.mutate()}>
            Atualizar
          </SmallButton>
        </div>
      </div>

      {v.analysis_state === 'none' && <p className="text-xs text-text-secondary">{analysisStateLabel.none}. A análise roda automaticamente em segundo plano.</p>}
      {v.analysis_state === 'failed' && <p className="text-xs text-status-danger">{analysisStateLabel.failed}.</p>}
      {v.analysis_state === 'empty' && <p className="text-xs text-text-secondary">{analysisStateLabel.empty} na última janela analisada.</p>}

      {refreshError && (
        <p role="alert" className="mt-1 mb-2 text-xs text-status-danger">
          {refreshError}
        </p>
      )}

      {v.summary && <p className="mb-3 text-sm text-text-primary break-words">{v.summary}</p>}

      {v.updated_at && <p className="mb-3 text-xs text-text-tertiary">Atualizado {formatWhen(v.updated_at)}</p>}

      {v.needs_customer_account_selection && (
        <p className="mb-3 rounded-control border border-status-warning bg-status-warning-soft px-2 py-1.5 text-xs text-text-primary">
          Este contato pertence a mais de uma empresa — a conta correta ainda não foi escolhida para este assunto.
        </p>
      )}

      {v.pending_actions.length > 0 && (
        <div className="mb-3">
          <p className="mb-1.5 text-xs font-medium text-text-secondary">Pendências identificadas ({v.pending_actions.length})</p>
          <ul className="space-y-1.5">
            {v.pending_actions.map((f) => (
              <FactRow key={f.id} fact={f} />
            ))}
          </ul>
        </div>
      )}

      {v.suggestions.length > 0 && (
        <div className="mb-3">
          <p className="mb-1.5 text-xs font-medium text-text-secondary">Sugestões ({v.suggestions.length})</p>
          <ul className="space-y-1.5">
            {v.suggestions.map((f) => (
              <FactRow key={f.id} fact={f} />
            ))}
          </ul>
        </div>
      )}

      {hasContent && v.topics.length > 0 && (
        <div>
          <p className="mb-1.5 text-xs font-medium text-text-secondary">Por assunto</p>
          <ul className="space-y-2">
            {v.topics.map((t) => (
              <li key={t.topic_id}>
                <p className="text-xs font-semibold text-text-primary">{t.title}</p>
                <ul className="mt-1 space-y-1.5">
                  {t.facts.map((f) => (
                    <FactRow key={f.id} fact={f} />
                  ))}
                </ul>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
