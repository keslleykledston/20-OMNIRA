import React, { useEffect, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { copilotWarningLabel, CopilotDraft, topicsAPI } from '../../lib/topics'
import { Chip, SectionTitle, SmallButton, describeError, isOff } from './shared'

// The copilot only DRAFTS. Nothing here sends: the attendant reads the draft and its warnings, copies it, edits it and
// sends it through the normal composer. A switched-off or unavailable copilot is simply not offered.
export function CopilotSection({ topicId }: { topicId: string }) {
  const [draft, setDraft] = useState<CopilotDraft | null>(null)
  const [error, setError] = useState('')
  const [off, setOff] = useState(false)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    setDraft(null)
    setError('')
    setCopied(false)
  }, [topicId])

  const suggest = useMutation({
    mutationFn: () => topicsAPI.suggestReply(topicId),
    onSuccess: (d) => {
      setDraft(d)
      setError('')
      setCopied(false)
    },
    onError: (e) => {
      if (isOff(e)) setOff(true)
      else setError(describeError(e, 'Não foi possível sugerir uma resposta agora.'))
    },
  })

  if (off) return null
  const copy = async () => {
    if (!draft) return
    try {
      await navigator.clipboard.writeText(draft.reply)
      setCopied(true)
    } catch {
      setCopied(false)
    }
  }
  return (
    <section aria-label="Copiloto de resposta">
      <SectionTitle>Copiloto de resposta</SectionTitle>
      <SmallButton disabled={suggest.isPending} onClick={() => suggest.mutate()}>
        {suggest.isPending ? 'Pensando…' : draft ? 'Sugerir outra resposta' : 'Sugerir resposta'}
      </SmallButton>
      {error && (
        <p role="alert" className="mt-2 text-xs text-status-danger">
          {error}
        </p>
      )}
      {draft && (
        <div className="mt-2 space-y-2 rounded-control border border-border-subtle p-2">
          <p className="text-[11px] font-medium text-status-warning">Rascunho sugerido por IA — não foi enviado. Revise antes de enviar.</p>
          <p className="text-sm text-text-primary whitespace-pre-wrap break-words" data-testid="copilot-draft">
            {draft.reply}
          </p>
          {draft.needs_human && <Chip tone="warning">Pede decisão de uma pessoa</Chip>}
          {draft.warnings.length > 0 && (
            <ul className="space-y-1" aria-label="Avisos do rascunho">
              {draft.warnings.map((w) => (
                <li key={w} className="text-xs text-status-warning">
                  ⚠ {copilotWarningLabel[w] ?? w}
                </li>
              ))}
            </ul>
          )}
          {draft.missing_info.length > 0 && (
            <div>
              <p className="text-[11px] font-medium text-text-tertiary">Falta saber</p>
              <ul className="list-disc pl-4 text-xs text-text-secondary">
                {draft.missing_info.map((m) => (
                  <li key={m}>{m}</li>
                ))}
              </ul>
            </div>
          )}
          <SmallButton onClick={copy}>{copied ? 'Copiado' : 'Copiar texto'}</SmallButton>
        </div>
      )}
    </section>
  )
}
