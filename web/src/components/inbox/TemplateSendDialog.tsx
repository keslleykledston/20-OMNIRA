import { useMemo, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Button, Input, Modal } from '../../components/primitives'
import { getTenantId } from '../../lib/session'
import {
  paramError,
  renderTemplate,
  sendTemplate,
  templateErrorMessage,
  useLineTemplates,
  type LineTemplate,
} from '../../lib/templates'

interface Props {
  open: boolean
  conversationId: string
  connectionId?: string
  contactName?: string
  onClose: () => void
}

/**
 * Sends an approved WhatsApp template (Meta Cloud). It is the way to write outside the 24 h window or to start with
 * someone who has not written to this number. Only approved templates this version can fill are offered; the others are
 * listed with the reason so nobody wonders where they went.
 */
export function TemplateSendDialog({ open, conversationId, connectionId, contactName, onClose }: Props) {
  const templates = useLineTemplates(connectionId, open)
  const queryClient = useQueryClient()
  const [pickedId, setPickedId] = useState<string>('')
  const [values, setValues] = useState<string[]>([])
  const [touched, setTouched] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // one Idempotency-Key per distinct request: a retry after a network failure reuses it, so nothing is queued twice
  const attempt = useRef<{ sig: string; key: string } | null>(null)

  const all = templates.data ?? []
  const usable = all.filter((t) => t.sendable)
  const blocked = all.filter((t) => !t.sendable)
  const picked: LineTemplate | undefined = usable.find((t) => t.id === pickedId)

  const pick = (t: LineTemplate | undefined) => {
    setPickedId(t?.id ?? '')
    setValues(Array.from({ length: t?.variable_count ?? 0 }, () => ''))
    setTouched(false)
    setError(null)
  }
  const preview = useMemo(() => (picked ? renderTemplate(picked.body, values) : ''), [picked, values])
  const invalid = picked ? values.some((v) => paramError(v)) : true

  const send = useMutation({
    mutationFn: () => {
      const sig = `${picked!.id}\n${values.join('\u001f')}`
      if (attempt.current?.sig !== sig) attempt.current = { sig, key: crypto.randomUUID() }
      return sendTemplate(conversationId, picked!.id, values.map((v) => v.trim()), attempt.current.key)
    },
    onSuccess: () => {
      attempt.current = null
      const tenantId = getTenantId()
      void queryClient.invalidateQueries({ queryKey: ['inbox-messages', tenantId, conversationId] })
      void queryClient.invalidateQueries({ queryKey: ['conversation-channel', tenantId, conversationId] })
      void queryClient.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] })
      pick(undefined)
      onClose()
    },
    onError: (e) => setError(templateErrorMessage(e)),
  })

  return (
    <Modal
      open={open}
      title="Enviar template"
      description={`Mensagem aprovada pela Meta${contactName ? ` para ${contactName}` : ''}. Vale dentro ou fora da janela de 24 h.`}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancelar
          </Button>
          <Button
            variant="primary"
            disabled={!picked || invalid || send.isPending}
            onClick={() => {
              setTouched(true)
              if (!invalid) send.mutate()
            }}
          >
            {send.isPending ? 'Enviando…' : 'Enviar template'}
          </Button>
        </>
      }
    >
      {templates.isLoading && <p className="text-sm text-text-secondary">Carregando templates…</p>}
      {templates.isError && (
        <p role="alert" className="text-sm text-status-danger">
          Não foi possível carregar os templates deste canal.
        </p>
      )}
      {!templates.isLoading && !templates.isError && usable.length === 0 && (
        <p className="text-sm text-text-secondary">
          Nenhum template aprovado disponível neste canal. Peça a um administrador para criar o template no Gerenciador do WhatsApp (Meta) e usar{' '}
          <strong>Sincronizar templates</strong> em Canais.
        </p>
      )}
      {usable.length > 0 && (
        <div className="space-y-4">
          <label className="block text-sm font-medium text-text-primary">
            Template
            <select
              value={pickedId}
              onChange={(e) => pick(usable.find((t) => t.id === e.target.value))}
              className="mt-1 w-full rounded-control border border-border-subtle bg-surface px-2.5 py-2 text-sm text-text-primary focus:outline-none focus:ring-2 focus:ring-accent-primary"
            >
              <option value="">Escolha um template…</option>
              {usable.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name} · {t.language}
                </option>
              ))}
            </select>
          </label>
          {picked && (
            <>
              {values.map((v, i) => (
                <Input
                  key={i}
                  label={`Variável {{${i + 1}}}`}
                  value={v}
                  autoComplete="off"
                  error={touched ? (paramError(v) ?? undefined) : undefined}
                  onChange={(e) => setValues((cur) => cur.map((x, j) => (j === i ? e.target.value : x)))}
                />
              ))}
              <div>
                <p className="mb-1 text-xs font-semibold uppercase text-text-tertiary">Como o contato vai ler</p>
                <p data-testid="template-preview" className="whitespace-pre-wrap rounded-control bg-surface-muted p-3 text-sm text-text-primary">
                  {preview}
                </p>
              </div>
            </>
          )}
          {error && (
            <p role="alert" className="rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">
              {error}
            </p>
          )}
        </div>
      )}
      {blocked.length > 0 && (
        <details className="mt-4 text-xs text-text-secondary">
          <summary className="cursor-pointer">{blocked.length} aprovado(s) que ainda não dá para enviar por aqui</summary>
          <ul className="mt-1 list-disc space-y-0.5 pl-5">
            {blocked.map((t) => (
              <li key={t.id}>
                {t.name} · {t.language}: {t.unsupported_reason || 'não suportado'}
              </li>
            ))}
          </ul>
        </details>
      )}
    </Modal>
  )
}
