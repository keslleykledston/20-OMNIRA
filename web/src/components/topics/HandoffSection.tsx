import React, { useEffect, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { HandoffInvite, topicsAPI } from '../../lib/topics'
import { SectionTitle, SmallButton, describeError, isOff } from './shared'

// Private handoff: the code is shown ONCE (the server keeps only a hash). It lives in component state only, never in
// storage, and disappears when the topic changes or the panel closes.
export function HandoffSection({ topicId }: { topicId: string }) {
  const [invite, setInvite] = useState<HandoffInvite | null>(null)
  const [error, setError] = useState('')
  const [off, setOff] = useState(false)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    setInvite(null)
    setError('')
    setCopied(false)
  }, [topicId])

  const create = useMutation({
    mutationFn: () => topicsAPI.createHandoff(topicId),
    onSuccess: (d) => {
      setInvite(d)
      setError('')
    },
    onError: (e) => {
      if (isOff(e)) setOff(true)
      else setError(describeError(e, 'Não foi possível criar o convite.'))
    },
  })
  if (off) return null
  const copy = async () => {
    if (!invite) return
    try {
      await navigator.clipboard.writeText(invite.token)
      setCopied(true)
    } catch {
      setCopied(false)
    }
  }
  return (
    <section aria-label="Convite para chat privado">
      <SectionTitle>Continuar no chat privado</SectionTitle>
      <SmallButton disabled={create.isPending} onClick={() => create.mutate()}>
        {create.isPending ? 'Criando…' : 'Gerar convite'}
      </SmallButton>
      {error && (
        <p role="alert" className="mt-2 text-xs text-status-danger">
          {error}
        </p>
      )}
      {invite && (
        <div className="mt-2 space-y-2 rounded-control border border-border-subtle p-2">
          <p className="text-xs text-text-secondary">{invite.instructions}</p>
          <code className="block break-all rounded-control bg-surface-muted p-2 text-xs text-text-primary" data-testid="handoff-token">
            {invite.token}
          </code>
          <p className="text-[11px] text-status-warning">Este código aparece só agora e vale uma única vez.</p>
          <SmallButton onClick={copy}>{copied ? 'Copiado' : 'Copiar código'}</SmallButton>
        </div>
      )}
    </section>
  )
}
