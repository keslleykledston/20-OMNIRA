import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, Card, CardBody, CardHeader, Input } from '../primitives'
import {
  aiErrorMessage,
  aiIntegrationAPI,
  validateAPIKey,
  validateBudget,
  type AIIntegration,
  type AITest,
} from '../../lib/aiIntegration'
import { getTenantId } from '../../lib/session'

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString('pt-BR', { dateStyle: 'short', timeStyle: 'short' }) : '')

// External AI (Gemini) for images and scanned PDFs. Off by default; an administrator supplies the key, accepts the
// data-sharing term and switches it on. Audio is always transcribed on this server and never uses this.
// Shown only to administrators (the API requires tenant.manage for every call).
export default function AIIntegrationCard() {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const { data, isLoading, isError } = useQuery({
    queryKey: ['ai-integration', tenantId],
    queryFn: aiIntegrationAPI.get,
  })

  const [apiKey, setApiKey] = useState('')
  const [model, setModel] = useState<string | null>(null)
  const [budget, setBudget] = useState<string | null>(null)
  const [accept, setAccept] = useState(false)
  const [message, setMessage] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)
  const [test, setTest] = useState<AITest | null>(null)

  const apply = (next: AIIntegration, text: string) => {
    queryClient.setQueryData(['ai-integration', tenantId], next)
    setApiKey('')
    setModel(null)
    setBudget(null)
    setMessage({ kind: 'ok', text })
  }
  const fail = (err: unknown) => setMessage({ kind: 'error', text: aiErrorMessage(err) })

  const update = useMutation({ mutationFn: aiIntegrationAPI.update, onError: fail })
  const removeKey = useMutation({
    mutationFn: aiIntegrationAPI.removeKey,
    onSuccess: (n) => {
      apply(n, 'Chave removida. A integração foi desativada.')
      setTest(null)
    },
    onError: fail,
  })
  const runTest = useMutation({
    mutationFn: aiIntegrationAPI.test,
    onSuccess: (t) => {
      setTest(t)
      setMessage(null)
    },
    onError: fail,
  })

  if (isLoading) return null
  if (isError || !data) {
    return (
      <Card>
        <CardBody>
          <p role="alert" className="text-sm text-status-danger">Não foi possível carregar a integração de IA.</p>
        </CardBody>
      </Card>
    )
  }

  const busy = update.isPending || removeKey.isPending || runTest.isPending
  const keyError = apiKey ? validateAPIKey(apiKey) : null
  const shownModel = model ?? data.model
  const shownBudget = budget ?? String(data.monthly_budget_usd).replace('.', ',')
  const budgetError = validateBudget(shownBudget)
  const adjustDirty = shownModel !== data.model || Number(shownBudget.replace(',', '.')) !== data.monthly_budget_usd
  const needsConsent = !data.consent_current
  const canEnable = data.key_configured && (!needsConsent || accept)
  const lastTest = test ?? data.last_test ?? null

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-base font-semibold text-text-primary">Inteligência artificial externa (Gemini)</h2>
          <Badge variant={data.enabled ? 'success' : 'default'} size="sm">{data.enabled ? 'Ativa' : 'Desativada'}</Badge>
        </div>
      </CardHeader>
      <CardBody>
        <p className="mb-5 text-sm text-text-secondary">
          Permite ler imagens e PDFs escaneados recebidos nas conversas (descrição e texto extraído). Fica desligada por
          padrão. Os áudios são transcritos no próprio servidor e nunca usam esta integração.
        </p>

        <div className="flex flex-col gap-6">
          <section aria-labelledby="ai-key-title" className="flex flex-col gap-3">
            <h3 id="ai-key-title" className="text-sm font-semibold text-text-primary">Chave da API</h3>
            {data.key_configured ? (
              <p className="text-sm text-text-secondary">
                Chave configurada{data.key_set_at ? ` em ${when(data.key_set_at)}` : ''}
                {data.key_set_by ? ` por ${data.key_set_by}` : ''}. Por segurança ela não pode ser exibida novamente.
              </p>
            ) : (
              <p className="text-sm text-text-secondary">Nenhuma chave configurada. Use uma chave de uma conta com faturamento ativo.</p>
            )}
            <form
              onSubmit={(e) => {
                e.preventDefault()
                if (!apiKey || keyError || busy) return
                update.mutate({ api_key: apiKey.trim() }, { onSuccess: (n) => apply(n, 'Chave salva.') })
              }}
              className="flex flex-col gap-3 sm:flex-row sm:items-end"
            >
              <div className="min-w-0 flex-1">
                <Input
                  id="ai-api-key"
                  label={data.key_configured ? 'Substituir chave' : 'Chave da API do Gemini'}
                  type="password"
                  autoComplete="new-password"
                  autoCorrect="off"
                  spellCheck={false}
                  value={apiKey}
                  disabled={busy}
                  error={keyError ?? undefined}
                  onChange={(e) => setApiKey(e.target.value)}
                />
              </div>
              <Button type="submit" disabled={!apiKey || keyError !== null || busy} isLoading={update.isPending && !!apiKey}>
                Salvar chave
              </Button>
            </form>
            {data.key_configured && (
              <div className="flex flex-wrap items-center gap-2">
                <Button type="button" variant="secondary" disabled={busy} isLoading={runTest.isPending} onClick={() => runTest.mutate()}>
                  Testar conexão
                </Button>
                <Button type="button" variant="tertiary" disabled={busy} onClick={() => removeKey.mutate()}>
                  Remover chave
                </Button>
              </div>
            )}
            {lastTest && (
              <p role="status" className={lastTest.ok ? 'text-sm text-status-success' : 'text-sm text-status-danger'}>
                {lastTest.message}
                {lastTest.ok && lastTest.model_available === false ? ' Ajuste o nome do modelo abaixo.' : ''}
                <span className="ml-2 text-xs text-text-tertiary">({when(lastTest.at)})</span>
              </p>
            )}
          </section>

          <section aria-labelledby="ai-adjust-title" className="flex flex-col gap-3">
            <h3 id="ai-adjust-title" className="text-sm font-semibold text-text-primary">Modelo e orçamento</h3>
            <form
              onSubmit={(e) => {
                e.preventDefault()
                if (!adjustDirty || budgetError || busy) return
                update.mutate(
                  { model: shownModel.trim(), monthly_budget_usd: Number(shownBudget.replace(',', '.')) },
                  { onSuccess: (n) => apply(n, 'Ajustes salvos.') },
                )
              }}
              className="grid grid-cols-1 gap-4 sm:grid-cols-2"
            >
              <Input id="ai-model" label="Modelo" value={shownModel} disabled={busy} onChange={(e) => setModel(e.target.value)} helperText="Ex.: gemini-2.5-flash" />
              <Input
                id="ai-budget"
                label="Orçamento mensal (US$)"
                inputMode="decimal"
                value={shownBudget}
                disabled={busy}
                error={adjustDirty ? budgetError ?? undefined : undefined}
                onChange={(e) => setBudget(e.target.value)}
                helperText="Teto de gasto por mês; ao atingir, a análise para até o mês virar."
              />
              <div className="sm:col-span-2">
                <Button type="submit" disabled={!adjustDirty || budgetError !== null || busy}>Salvar ajustes</Button>
              </div>
            </form>
          </section>

          <section aria-labelledby="ai-consent-title" className="flex flex-col gap-3">
            <h3 id="ai-consent-title" className="text-sm font-semibold text-text-primary">Ativação</h3>
            <div className="rounded border border-border-subtle bg-surface-subtle p-3 text-sm text-text-secondary">{data.consent_text}</div>
            {needsConsent ? (
              <label className="flex items-start gap-2 text-sm text-text-primary">
                <input
                  type="checkbox"
                  className="mt-0.5"
                  checked={accept}
                  disabled={busy}
                  onChange={(e) => setAccept(e.target.checked)}
                />
                <span>Li e aceito este termo em nome da organização.</span>
              </label>
            ) : (
              <p className="text-xs text-text-tertiary">
                Termo aceito{data.consent_by ? ` por ${data.consent_by}` : ''}
                {data.consent_at ? ` em ${when(data.consent_at)}` : ''}.
              </p>
            )}
            {data.enabled ? (
              <div>
                <Button
                  type="button"
                  variant="secondary"
                  disabled={busy}
                  isLoading={update.isPending && apiKey === ''}
                  onClick={() => update.mutate({ enabled: false }, { onSuccess: (n) => apply(n, 'Integração desativada.') })}
                >
                  Desativar
                </Button>
              </div>
            ) : (
              <div className="flex flex-wrap items-center gap-3">
                <Button
                  type="button"
                  disabled={!canEnable || busy}
                  isLoading={update.isPending && apiKey === ''}
                  onClick={() =>
                    update.mutate(
                      { enabled: true, ...(needsConsent ? { accept_external_ai: true } : {}) },
                      { onSuccess: (n) => apply(n, 'Integração ativada.') },
                    )
                  }
                >
                  Ativar integração
                </Button>
                {!data.key_configured && <span className="text-xs text-text-tertiary">Salve a chave para poder ativar.</span>}
                {data.key_configured && needsConsent && !accept && <span className="text-xs text-text-tertiary">Aceite o termo para poder ativar.</span>}
              </div>
            )}
          </section>

          {message && (
            <p role={message.kind === 'error' ? 'alert' : 'status'} className={message.kind === 'error' ? 'text-sm text-status-danger' : 'text-sm text-status-success'}>
              {message.text}
            </p>
          )}
        </div>
      </CardBody>
    </Card>
  )
}
