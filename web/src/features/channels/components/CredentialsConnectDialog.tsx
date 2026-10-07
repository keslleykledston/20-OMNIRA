import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Button, Icon, Input, Modal } from '../../../components/primitives'
import { syncTemplates } from '../../../lib/templates'
import {
  integrationErrorMessage,
  integrationsAPI,
  type ChannelConnection,
  type ProviderDescriptor,
} from '../../../lib/integrations'

interface Props {
  open: boolean
  provider: ProviderDescriptor | null
  /** When set, shows the webhook setup of an existing connection instead of the creation form. */
  connection?: ChannelConnection | null
  onClose: () => void
  onChanged: () => void
}

function CopyRow({ label, value, help }: { label: string; value: string; help?: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* the value is selectable text; copying by hand still works */
    }
  }
  return (
    <div>
      <div className="text-body-sm font-medium text-text-primary">{label}</div>
      <div className="mt-1 flex items-center gap-2">
        <code className="min-w-0 flex-1 break-all rounded-card bg-surface-muted px-3 py-2 text-body-sm select-all">{value}</code>
        <Button variant="secondary" size="sm" onClick={copy}>
          {copied ? 'Copiado' : 'Copiar'}
        </Button>
      </div>
      {help && <p className="mt-1 text-body-sm text-text-secondary">{help}</p>}
    </div>
  )
}

/**
 * Descriptor-driven credentials form (official WhatsApp / Meta). Secrets are typed here only, go straight to the
 * server's encrypted store and are never shown again; what comes back is the callback URL and verify token to paste
 * into the Meta app.
 */
export function CredentialsConnectDialog({ open, provider, connection, onClose, onChanged }: Props) {
  const [values, setValues] = useState<Record<string, string>>({})
  const [created, setCreated] = useState<ChannelConnection | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)

  const active = connection ?? created
  const reset = () => {
    setValues({})
    setCreated(null)
    setError(null)
    setNote(null)
  }
  const close = () => {
    reset()
    onClose()
  }

  const create = useMutation({
    mutationFn: () => integrationsAPI.create(provider!.id, values, false),
    onSuccess: (c) => {
      setError(null)
      setValues({}) // secrets leave the page state as soon as they are sent
      setCreated(c)
      onChanged()
    },
    onError: (e) => setError(integrationErrorMessage(e, 'Não foi possível salvar a conexão.')),
  })

  const sync = useMutation({
    mutationFn: (id: string) => syncTemplates(id),
    onSuccess: (r) => {
      setError(null)
      setNote(
        r.synced === 0
          ? 'Nenhum template encontrado nesta conta. Crie e aprove templates no Gerenciador do WhatsApp (Meta) e sincronize de novo.'
          : `${r.synced} template(s) sincronizado(s); ${r.sendable} pronto(s) para enviar.`,
      )
    },
    onError: (e) => {
      setNote(null)
      setError(
        (e as { response?: { status?: number } })?.response?.status === 422
          ? 'A Meta recusou o token. Teste a conexão e confira o token.'
          : integrationErrorMessage(e, 'Não foi possível sincronizar os templates.'),
      )
    },
  })

  const test = useMutation({
    mutationFn: (id: string) => integrationsAPI.test(id),
    onSuccess: (c) => {
      setError(null)
      setCreated(c)
      const subscribed = c.displays?.webhook_subscribed
      setNote(
        c.status === 'active'
          ? `Conexão validada com a Meta${c.displays?.verified_name ? ` (${c.displays.verified_name})` : ''}.` +
              (subscribed === 'false' ? ' Atenção: o webhook ainda não está assinado (campo "messages").' : '')
          : 'A Meta recusou a credencial.',
      )
      onChanged()
    },
    onError: (e) => {
      setNote(null)
      setError(
        (e as { response?: { status?: number } })?.response?.status === 422
          ? 'A Meta recusou o token. Gere um token permanente do usuário de sistema e tente de novo.'
          : integrationErrorMessage(e, 'Não foi possível testar a conexão.'),
      )
      onChanged()
    },
  })

  if (!provider) return null
  const missing = provider.inputs.some((i) => i.required && !(values[i.key] ?? '').trim())
  const displays = active?.displays ?? {}

  return (
    <Modal
      open={open}
      title={active ? `Configurar webhook — ${provider.name}` : `Conectar ${provider.name}`}
      description={
        active
          ? 'Cole estes valores no app da Meta (WhatsApp → Configuration → Webhook) e assine o campo "messages".'
          : 'Os segredos são guardados cifrados e nunca são exibidos de novo.'
      }
      onClose={close}
      footer={
        active ? (
          <>
            <Button variant="secondary" onClick={close}>
              Fechar
            </Button>
            <Button variant="secondary" onClick={() => sync.mutate(active.id)} disabled={sync.isPending}>
              {sync.isPending ? 'Sincronizando…' : 'Sincronizar templates'}
            </Button>
            <Button variant="primary" onClick={() => test.mutate(active.id)} disabled={test.isPending}>
              {test.isPending ? 'Testando…' : 'Testar conexão'}
            </Button>
          </>
        ) : (
          <>
            <Button variant="secondary" onClick={close}>
              Cancelar
            </Button>
            <Button variant="primary" onClick={() => create.mutate()} disabled={missing || create.isPending}>
              {create.isPending ? 'Salvando…' : 'Salvar conexão'}
            </Button>
          </>
        )
      }
    >
      {error && (
        <div role="alert" className="mb-3 flex items-start gap-2 rounded-card bg-status-danger-soft p-3 text-body-sm text-status-danger">
          <Icon name="info" size={18} />
          <span>{error}</span>
        </div>
      )}
      {note && (
        <div role="status" className="mb-3 rounded-card bg-status-success-soft p-3 text-body-sm text-status-success">
          {note}
        </div>
      )}
      {active ? (
        <div className="space-y-4">
          {displays.callback_url && <CopyRow label="URL de retorno de chamada (Callback URL)" value={displays.callback_url} />}
          {displays.verify_token && <CopyRow label="Token de verificação (Verify Token)" value={displays.verify_token} />}
          {displays.display_phone_number && (
            <p className="text-body-sm text-text-secondary">
              Número: <strong>{displays.display_phone_number}</strong>
              {displays.quality_rating ? ` · qualidade ${displays.quality_rating}` : ''}
            </p>
          )}
          <p className="text-body-sm text-text-secondary">
            Depois de salvar na Meta, clique em “Testar conexão”. Fora da janela de 24 h a Meta só aceita mensagens de template.
          </p>
        </div>
      ) : (
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (!missing) create.mutate()
          }}
        >
          {provider.inputs.map((i) => (
            <Input
              key={i.key}
              label={i.label}
              type={i.secret ? 'password' : 'text'}
              autoComplete="off"
              spellCheck={false}
              value={values[i.key] ?? ''}
              placeholder={i.example}
              helperText={i.help}
              onChange={(e) => setValues((v) => ({ ...v, [i.key]: e.target.value }))}
            />
          ))}
        </form>
      )}
    </Modal>
  )
}
