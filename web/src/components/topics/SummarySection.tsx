import React, { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getTenantId } from '../../lib/session'
import { summaryStatusLabel, topicsAPI, TopicSummary } from '../../lib/topics'
import { Chip, SectionTitle, SmallButton, describeError } from './shared'

// The summary of a topic is VERSIONED: a person's correction is a new version, never an overwrite, and a machine summary
// never replaces it. The newest version is shown; the history is one click away.
export function SummarySection({ topicId, canManage }: { topicId: string; canManage: boolean }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const key = ['topic-summaries', tenantId, topicId]
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [showHistory, setShowHistory] = useState(false)
  const [error, setError] = useState('')

  const list = useQuery({ queryKey: key, queryFn: () => topicsAPI.summaries(topicId), retry: false })
  const refresh = () => qc.invalidateQueries({ queryKey: key })
  const fail = (e: unknown) => setError(describeError(e, 'Não foi possível concluir a ação.'))

  const generate = useMutation({ mutationFn: () => topicsAPI.generateSummary(topicId), onSuccess: () => { setError(''); refresh() }, onError: fail })
  const confirm = useMutation({ mutationFn: () => topicsAPI.confirmSummary(topicId), onSuccess: () => { setError(''); refresh() }, onError: fail })
  const correct = useMutation({
    mutationFn: (text: string) => topicsAPI.correctSummary(topicId, text),
    onSuccess: () => { setEditing(false); setError(''); refresh() },
    onError: fail,
  })

  const items: TopicSummary[] = list.data ?? []
  const current = items[0]
  const busy = generate.isPending || confirm.isPending || correct.isPending

  return (
    <section aria-label="Resumo do assunto" className="space-y-2">
      <SectionTitle>Resumo do assunto</SectionTitle>
      {list.isLoading && <p className="text-xs text-text-tertiary">Carregando…</p>}
      {!list.isLoading && !current && <p className="text-xs text-text-secondary">Ainda não há resumo deste assunto.</p>}
      {current && !editing && (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <Chip tone={current.status === 'ai_inferred' ? 'warning' : 'success'}>{summaryStatusLabel[current.status]}</Chip>
            <span className="text-[11px] text-text-tertiary">v{current.version}</span>
          </div>
          <p className="text-sm text-text-primary whitespace-pre-wrap break-words">{current.summary_text}</p>
          {current.status === 'ai_inferred' && (
            <p className="text-[11px] text-text-tertiary">Gerado automaticamente a partir das mensagens deste assunto. Confira antes de confiar.</p>
          )}
        </div>
      )}
      {editing && (
        <div className="space-y-2">
          <textarea
            aria-label="Texto corrigido do resumo"
            className="w-full rounded-control border border-border-subtle bg-surface p-2 text-sm text-text-primary"
            rows={5}
            maxLength={4000}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
          />
          <div className="flex gap-2">
            <SmallButton tone="primary" disabled={busy || draft.trim() === ''} onClick={() => correct.mutate(draft)}>
              Salvar como nova versão
            </SmallButton>
            <SmallButton onClick={() => setEditing(false)}>Cancelar</SmallButton>
          </div>
        </div>
      )}
      {canManage && !editing && (
        <div className="flex flex-wrap gap-2">
          {current && current.status === 'ai_inferred' && (
            <SmallButton tone="primary" disabled={busy} onClick={() => confirm.mutate()}>
              Confirmar resumo
            </SmallButton>
          )}
          {current && (
            <SmallButton
              disabled={busy}
              onClick={() => {
                setDraft(current.summary_text)
                setEditing(true)
              }}
            >
              Corrigir
            </SmallButton>
          )}
          <SmallButton disabled={busy} onClick={() => generate.mutate()}>
            {generate.isPending ? 'Gerando…' : current ? 'Gerar novamente' : 'Gerar resumo'}
          </SmallButton>
        </div>
      )}
      {error && (
        <p role="alert" className="text-xs text-status-danger">
          {error}
        </p>
      )}
      {items.length > 1 && (
        <div>
          <button type="button" className="text-xs font-medium text-accent-primary hover:underline" onClick={() => setShowHistory((v) => !v)}>
            {showHistory ? 'Ocultar histórico' : `Ver histórico (${items.length} versões)`}
          </button>
          {showHistory && (
            <ul className="mt-2 space-y-2" aria-label="Histórico de versões">
              {items.slice(1).map((s) => (
                <li key={s.id} className="rounded-control border border-border-subtle p-2">
                  <div className="mb-1 flex items-center gap-1.5">
                    <Chip tone="neutral">{summaryStatusLabel[s.status]}</Chip>
                    <span className="text-[11px] text-text-tertiary">v{s.version}</span>
                  </div>
                  <p className="text-xs text-text-secondary whitespace-pre-wrap break-words">{s.summary_text}</p>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </section>
  )
}
