import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  ErrorState,
  Icon,
  Input,
} from '../components/primitives'
import {
  integrationErrorMessage,
  integrationsAPI,
  type ChannelConnection,
  type ProviderDescriptor,
} from '../lib/integrations'
import { getTenantId } from '../lib/session'
import { PROVIDER_KIND_LABEL, STATE_LABEL, STATE_VARIANT, formatAccount, toConnectionState } from '../features/channels/types'
import { WizardStepper } from '../features/channels/components/WizardStepper'
import { QRCodeView, QRInstructions } from '../features/channels/components/QRCodeView'
import {
  useConnectionQr,
  useLiveConnection,
  useRestartSession,
} from '../features/channels/data/useChannelSession'

const STEPS = [
  { id: 1, label: 'Configuração' },
  { id: 2, label: 'Conectar' },
  { id: 3, label: 'Escanear QR' },
  { id: 4, label: 'Concluído' },
]

export default function WahaWizardPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const providerId = params.get('provider') ?? 'waha'
  const resumeId = params.get('connection')

  const [connection, setConnection] = useState<ChannelConnection | null>(null)
  const [cancelOpen, setCancelOpen] = useState(false)

  const providers = useQuery({
    queryKey: ['channel-providers', getTenantId()],
    queryFn: () => integrationsAPI.providers(),
    retry: false,
  })
  const provider: ProviderDescriptor | undefined = useMemo(
    () => (providers.data ?? []).find((p) => p.id === providerId),
    [providers.data, providerId],
  )

  // Resuming a connection that already exists (card action "Detalhes").
  const resumed = useQuery({
    queryKey: ['channel-connection', getTenantId(), resumeId],
    queryFn: () => integrationsAPI.get(resumeId!),
    enabled: !!resumeId && !connection,
    retry: false,
  })
  useEffect(() => {
    if (resumed.data && !connection) setConnection(resumed.data)
  }, [resumed.data, connection])

  return connection ? (
    <ConnectedFlow
      connection={connection}
      provider={provider}
      onCancel={() => setCancelOpen(true)}
      cancelOpen={cancelOpen}
      onCancelDismiss={() => setCancelOpen(false)}
      onCancelConfirm={() => navigate('/channels')}
      navigate={navigate}
    />
  ) : (
    <Shell step={1} onBack={() => navigate('/channels')}>
      <ConfigureStep
        provider={provider}
        isLoading={providers.isLoading}
        error={
          providers.isError
            ? integrationErrorMessage(providers.error, 'Não foi possível carregar o provider.')
            : !providers.isLoading && !provider
              ? 'Provider não disponível para este tenant.'
              : null
        }
        onCreated={setConnection}
        onCancel={() => navigate('/channels')}
      />
    </Shell>
  )
}

function Shell({
  step,
  onBack,
  children,
}: {
  step: number
  onBack: () => void
  children: React.ReactNode
}) {
  return (
    <div className="px-6 py-6 lg:px-8 lg:py-8">
      <div className="mx-auto w-full max-w-[920px]">
        <button
          type="button"
          onClick={onBack}
          className="mb-4 inline-flex items-center gap-1.5 rounded-control text-body-sm font-medium text-accent-primary transition-colors hover:text-accent-primary-hover focus-visible:ring-2 focus-visible:ring-accent-primary"
        >
          <Icon name="arrow-left" size={16} />
          Voltar
        </button>

        <h1 className="text-display-md font-bold text-text-primary">
          Conectar WhatsApp (Não Oficial)
        </h1>
        <p className="mt-1 text-body-md text-text-secondary">
          Crie uma nova conexão via WAHA a partir do QR Code.
        </p>

        <div className="my-8">
          <WizardStepper steps={STEPS} current={step} />
        </div>

        {children}
      </div>
    </div>
  )
}

function ProviderSummary({ provider }: { provider?: ProviderDescriptor }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="flex h-10 w-10 items-center justify-center rounded-card bg-status-success-soft text-status-success">
        <Icon name="whatsapp" size={22} />
      </span>
      <span className="font-semibold text-text-primary">{provider?.name ?? 'WAHA'}</span>
      <Badge variant="default" size="sm">
        {PROVIDER_KIND_LABEL[provider?.kind ?? 'unofficial']}
      </Badge>
    </div>
  )
}

function ConfigureStep({
  provider,
  isLoading,
  error,
  onCreated,
  onCancel,
}: {
  provider?: ProviderDescriptor
  isLoading: boolean
  error: string | null
  onCreated: (c: ChannelConnection) => void
  onCancel: () => void
}) {
  const [values, setValues] = useState<Record<string, string>>({})
  const [acknowledged, setAcknowledged] = useState(false)

  const create = useMutation({
    mutationFn: async () => {
      const created = await integrationsAPI.create(provider!.id, values, acknowledged)
      // Start the gateway session right away so step 2 has something to show.
      try {
        return await integrationsAPI.start(created.id)
      } catch {
        return created
      }
    },
    onSuccess: onCreated,
  })

  // Only fields the provider actually declares — no decorative inputs.
  const inputs = provider?.inputs ?? []
  const missingRequired = inputs.some((i) => i.required && !values[i.key]?.trim())
  const needsAck = !!provider?.risk_notice && !acknowledged

  if (error) return <ErrorState message={error} />

  return (
    <Card padding="large">
      {isLoading ? (
        <p className="text-body-sm text-text-secondary">Carregando provider...</p>
      ) : (
        <>
          <ProviderSummary provider={provider} />

          {inputs.length > 0 && (
            <div className="mt-6 space-y-4">
              {inputs.map((field) => (
                <Input
                  key={field.key}
                  label={field.label + (field.required ? ' *' : '')}
                  type={field.secret ? 'password' : 'text'}
                  placeholder={field.example}
                  helperText={field.help}
                  value={values[field.key] ?? ''}
                  onChange={(e) => setValues((v) => ({ ...v, [field.key]: e.target.value }))}
                />
              ))}
            </div>
          )}

          {provider?.risk_notice && (
            <label className="mt-6 flex cursor-pointer items-start gap-3 rounded-card border border-status-warning-border bg-status-warning-soft p-4">
              <input
                type="checkbox"
                checked={acknowledged}
                onChange={(e) => setAcknowledged(e.target.checked)}
                className="mt-0.5 h-4 w-4 flex-shrink-0 accent-[color:var(--color-accent-primary)]"
              />
              <span role="alert" className="text-body-sm text-text-secondary">
                {provider.risk_notice}
              </span>
            </label>
          )}

          {create.isError && (
            <div className="mt-4">
              <ErrorState
                message={integrationErrorMessage(create.error, 'Não foi possível criar a conexão.')}
              />
            </div>
          )}

          <div className="mt-8 flex flex-col gap-2 sm:flex-row sm:justify-end">
            <Button variant="secondary" onClick={onCancel} disabled={create.isPending}>
              Cancelar
            </Button>
            <Button
              variant="primary"
              onClick={() => create.mutate()}
              disabled={!provider || missingRequired || needsAck}
              isLoading={create.isPending}
            >
              Continuar
            </Button>
          </div>
        </>
      )}
    </Card>
  )
}

function ConnectedFlow({
  connection,
  provider,
  onCancel,
  cancelOpen,
  onCancelDismiss,
  onCancelConfirm,
  navigate,
}: {
  connection: ChannelConnection
  provider?: ProviderDescriptor
  onCancel: () => void
  cancelOpen: boolean
  onCancelDismiss: () => void
  onCancelConfirm: () => void
  navigate: (to: string) => void
}) {
  const { connection: live, state } = useLiveConnection(connection)
  const qr = useConnectionQr(connection.id, state)
  const restart = useRestartSession(connection.id)

  const step = state === 'connected' ? 4 : state === 'qr_required' ? 3 : 2

  return (
    <Shell step={step} onBack={onCancel}>
      {step === 2 && (
        <Card padding="large">
          <div className="flex flex-col items-center py-10 text-center">
            {state === 'failed' ? (
              <ErrorState
                title="A sessão não pôde ser iniciada"
                message="O gateway não conseguiu abrir a sessão. Gere uma nova tentativa para continuar."
                action={{ label: 'Tentar novamente', onClick: () => restart.mutate() }}
              />
            ) : (
              <>
                <span className="h-10 w-10 animate-spin rounded-full border-2 border-accent-primary-soft border-t-accent-primary" />
                <p className="mt-4 text-body-lg font-semibold text-text-primary">
                  Preparando conexão...
                </p>
                <p className="mt-1 text-body-sm text-text-secondary">
                  Estamos abrindo a sessão no gateway. Isso costuma levar alguns segundos.
                </p>
                <div className="mt-6">
                  <Button variant="secondary" size="sm" onClick={() => restart.mutate()} isLoading={restart.isPending}>
                    Reiniciar tentativa
                  </Button>
                </div>
              </>
            )}
          </div>
        </Card>
      )}

      {step === 3 && (
        <Card padding="large">
          <div className="grid gap-8 lg:grid-cols-2">
            <QRCodeView
              qr={qr.data}
              errorMessage={qr.isError ? integrationErrorMessage(qr.error, 'QR indisponível.') : undefined}
            />

            <div className="flex flex-col justify-between gap-6">
              <QRInstructions />

              <div>
                <p
                  aria-live="polite"
                  data-testid="wizard-status"
                  className="flex items-center gap-2 text-body-sm font-medium text-text-primary"
                >
                  <span className="h-2 w-2 rounded-full bg-status-warning" />
                  Aguardando conexão...
                </p>
                <p className="mt-1 text-metadata">
                  O código se renova automaticamente a cada poucos segundos.
                </p>

                <div className="mt-4 flex flex-col gap-2 sm:flex-row">
                  <Button variant="secondary" size="sm" onClick={onCancel}>
                    Cancelar
                  </Button>
                  <Button
                    variant="tertiary"
                    size="sm"
                    onClick={() => restart.mutate()}
                    isLoading={restart.isPending}
                  >
                    Gerar novo QR
                  </Button>
                </div>
              </div>
            </div>
          </div>
        </Card>
      )}

      {step === 4 && (
        <Card padding="large">
          <div className="flex flex-col items-center py-6 text-center" data-testid="wizard-connected">
            <span className="flex h-14 w-14 items-center justify-center rounded-full bg-status-success-soft text-status-success">
              <Icon name="check" size={28} />
            </span>
            <h2 className="mt-4 text-section-lg font-semibold text-text-primary">
              WhatsApp conectado
            </h2>

            <dl className="mt-6 grid w-full max-w-sm gap-2 text-body-sm">
              <Row label="Número" value={formatAccount(live.external_account_id) ?? '—'} />
              <Row label="Provider" value={provider?.name ?? live.provider} />
              <Row label="Tipo" value={PROVIDER_KIND_LABEL[live.provider_kind]} />
              <Row
                label="Status"
                value={<Badge variant={STATE_VARIANT[state]} size="sm">{STATE_LABEL[state]}</Badge>}
              />
            </dl>

            <div className="mt-8 flex w-full max-w-sm flex-col gap-2 sm:flex-row">
              <Button variant="primary" className="flex-1" onClick={() => navigate('/inbox')}>
                Ir para conversas
              </Button>
              <Button variant="secondary" className="flex-1" onClick={() => navigate('/channels')}>
                Voltar aos canais
              </Button>
            </div>
          </div>
        </Card>
      )}

      <ConfirmDialog
        open={cancelOpen}
        title="Cancelar conexão"
        message="A sessão em andamento será abandonada. A conexão criada continuará listada em Canais e pode ser retomada."
        confirmLabel="Cancelar conexão"
        destructive
        onConfirm={onCancelConfirm}
        onCancel={onCancelDismiss}
      />
    </Shell>
  )
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-border-subtle py-2 last:border-b-0">
      <dt className="text-text-secondary">{label}</dt>
      <dd className="font-medium text-text-primary">{value}</dd>
    </div>
  )
}
