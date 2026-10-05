import React, { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getTenantId } from '../../lib/session'
import { topicsAPI } from '../../lib/topics'
import { Chip, SectionTitle, SmallButton, describeError, isOff } from './shared'

const sourceLabel: Record<string, string> = {
  topic_link: 'escolhida',
  ticket: 'do chamado',
  sole_company: 'única empresa do contato',
}

// Company context of the subject (ADR-0018). The context belongs to the TOPIC: one person can talk about several
// companies in one chat. A contact with several companies is never guessed: this section asks, and a person's click is
// what records the answer. Only a person's choice is stored; the other sources are shown as derived.
export function AccountSection({ topicId, canManage }: { topicId: string; canManage: boolean }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const key = ['topic-account-context', tenantId, topicId]
  const ctx = useQuery({ queryKey: key, queryFn: () => topicsAPI.accountContext(topicId), retry: false })
  const refresh = (data: unknown) => {
    qc.setQueryData(key, data)
    setError('')
  }
  const link = useMutation({
    mutationFn: (v: { accountId: string; relation: 'primary' | 'related' }) => topicsAPI.linkAccount(topicId, v.accountId, v.relation),
    onSuccess: refresh,
    onError: (e) => setError(describeError(e, 'Não foi possível registrar a empresa.')),
  })
  const unlink = useMutation({
    mutationFn: (accountId: string) => topicsAPI.unlinkAccount(topicId, accountId),
    onSuccess: refresh,
    onError: (e) => setError(describeError(e, 'Não foi possível remover a empresa.')),
  })

  // switched off or not visible to this user (no account.read): the section simply does not exist
  if (ctx.isLoading || (ctx.isError && (isOff(ctx.error) || (ctx.error as any)?.status === 403))) return null
  const data = ctx.data
  if (!data) return null
  const busy = link.isPending || unlink.isPending

  return (
    <section aria-label="Empresa do assunto">
      <SectionTitle>Empresa do assunto</SectionTitle>
      {data.status === 'none' && <p className="text-xs text-text-secondary">Nenhuma empresa definida para este assunto.</p>}

      {data.primary && (
        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          <span className="font-medium text-text-primary break-words">{data.primary.name}</span>
          <Chip tone="info">principal</Chip>
          <Chip tone="neutral">{sourceLabel[data.primary.source] ?? data.primary.source}</Chip>
          {!data.primary.linked_to_contact && <Chip tone="warning">fora das empresas do contato</Chip>}
          {canManage && !data.primary.persisted && (
            <SmallButton disabled={busy} onClick={() => link.mutate({ accountId: data.primary!.account_id, relation: 'primary' })}>
              Confirmar esta empresa
            </SmallButton>
          )}
          {canManage && data.primary.persisted && (
            <SmallButton disabled={busy} aria-label={`Remover ${data.primary.name}`} onClick={() => unlink.mutate(data.primary!.account_id)}>
              Remover
            </SmallButton>
          )}
        </div>
      )}

      {data.status === 'needs_choice' && (
        <div className="space-y-2 rounded-control border border-status-warning p-2" role="group" aria-label="Escolher a empresa do assunto">
          <p className="text-xs text-text-secondary">Este contato pertence a {data.candidates.length} empresas. De qual é este assunto?</p>
          <div className="flex flex-wrap gap-1.5">
            {data.candidates.map((c) => (
              <SmallButton key={c.account_id} disabled={!canManage || busy} onClick={() => link.mutate({ accountId: c.account_id, relation: 'primary' })}>
                {c.name}
              </SmallButton>
            ))}
          </div>
          {!canManage && <p className="text-[11px] text-text-tertiary">Quem atende a conversa escolhe a empresa.</p>}
        </div>
      )}

      {data.related.length > 0 && (
        <ul className="mt-2 space-y-1" aria-label="Empresas relacionadas">
          {data.related.map((r) => (
            <li key={r.account_id} className="flex items-center justify-between gap-2 text-xs">
              <span className="min-w-0 truncate text-text-secondary">
                {r.name} <Chip tone="neutral">relacionada</Chip>
              </span>
              {canManage && (
                <SmallButton disabled={busy} aria-label={`Remover ${r.name}`} onClick={() => unlink.mutate(r.account_id)}>
                  Remover
                </SmallButton>
              )}
            </li>
          ))}
        </ul>
      )}
      {error && (
        <p role="alert" className="mt-2 text-xs text-status-danger">
          {error}
        </p>
      )}
    </section>
  )
}
