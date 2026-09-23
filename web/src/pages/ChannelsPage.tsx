import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Icon,
  PageHeader,
  Tabs,
  type MenuAction,
} from '../components/primitives'
import {
  integrationErrorMessage,
  integrationsAPI,
  type ChannelConnection,
  type ProviderDescriptor,
} from '../lib/integrations'
import { getTenantId } from '../lib/session'
import { CHANNEL_TABS } from '../features/channels/types'
import {
  ChannelCardSkeleton,
  ChannelConnectionCard,
} from '../features/channels/components/ChannelConnectionCard'
import { UnofficialProviderCallout } from '../features/channels/components/InfoCallout'
import { AddChannelDialog } from '../features/channels/components/AddChannelDialog'
import { connectionsKey, useLiveConnection } from '../features/channels/data/useChannelSession'

export default function ChannelsPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const tenantId = getTenantId()

  const [tab, setTab] = useState<string>('all')
  const [addOpen, setAddOpen] = useState(false)
  const [pendingStop, setPendingStop] = useState<ChannelConnection | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  const providers = useQuery({
    queryKey: ['channel-providers', tenantId],
    queryFn: () => integrationsAPI.providers(),
    retry: false,
  })

  const connections = useQuery({
    queryKey: connectionsKey(),
    queryFn: () => integrationsAPI.list(),
    retry: false,
  })

  const providerById = useMemo(() => {
    const map = new Map<string, ProviderDescriptor>()
    for (const p of providers.data ?? []) map.set(p.id, p)
    return map
  }, [providers.data])

  const invalidate = () => queryClient.invalidateQueries({ queryKey: connectionsKey() })

  const start = useMutation({
    mutationFn: (id: string) => integrationsAPI.start(id),
    onSuccess: () => void invalidate(),
    onError: (e) => setActionError(integrationErrorMessage(e, 'Não foi possível iniciar a sessão.')),
  })

  const stop = useMutation({
    mutationFn: (id: string) => integrationsAPI.stop(id),
    onSuccess: () => {
      setPendingStop(null)
      void invalidate()
    },
    onError: (e) => {
      setPendingStop(null)
      setActionError(integrationErrorMessage(e, 'Não foi possível desconectar a sessão.'))
    },
  })

  const test = useMutation({
    mutationFn: (id: string) => integrationsAPI.test(id),
    onSuccess: () => void invalidate(),
    onError: (e) => setActionError(integrationErrorMessage(e, 'Não foi possível testar a conexão.')),
  })

  const visible = (connections.data ?? []).filter((c) => {
    if (tab === 'all') return true
    const channel = providerById.get(c.provider)?.channel
    return channel === tab
  })

  const onPickProvider = (p: ProviderDescriptor) => {
    setAddOpen(false)
    if (p.connect_method === 'qr_session') {
      navigate(`/channels/whatsapp/new?provider=${p.id}`)
    }
  }

  return (
    <div className="px-6 py-6 lg:px-8 lg:py-8">
      <PageHeader
        title="Canais"
        description="Conecte e gerencie seus canais de atendimento."
        className="mb-4 border-b-0 bg-transparent p-0"
        actions={
          <Button variant="primary" size="md" onClick={() => setAddOpen(true)}>
            <Icon name="plus" size={18} />
            Adicionar canal
          </Button>
        }
      />

      <Tabs items={CHANNEL_TABS} value={tab} onChange={setTab} aria-label="Filtrar por canal" className="mb-6" />

      {actionError && (
        <div className="mb-4">
          <ErrorState message={actionError} isDismissible onDismiss={() => setActionError(null)} />
        </div>
      )}

      {connections.isError ? (
        <ErrorState
          message={integrationErrorMessage(connections.error, 'Não foi possível carregar os canais.')}
          action={{ label: 'Tentar novamente', onClick: () => void connections.refetch() }}
        />
      ) : (
        <div className="space-y-4">
          {connections.isLoading ? (
            <>
              <ChannelCardSkeleton />
              <ChannelCardSkeleton />
            </>
          ) : visible.length === 0 ? (
            <EmptyState
              title="Nenhum canal conectado"
              description={
                tab === 'all'
                  ? 'Conecte um canal para começar a receber conversas.'
                  : 'Nenhuma conexão para este canal.'
              }
              action={{ label: 'Adicionar canal', onClick: () => setAddOpen(true) }}
            />
          ) : (
            visible.map((c) => (
              <LiveChannelConnectionCard
                key={c.id}
                connection={c}
                provider={providerById.get(c.provider)}
                onTest={(id) => test.mutate(id)}
                onStart={(id) => start.mutate(id)}
                onRequestStop={setPendingStop}
                testPending={test.isPending}
                startPending={start.isPending}
              />
            ))
          )}

          <UnofficialProviderCallout />
        </div>
      )}

      <AddChannelDialog
        open={addOpen}
        providers={providers.data ?? []}
        onSelect={onPickProvider}
        onClose={() => setAddOpen(false)}
      />

      <ConfirmDialog
        open={!!pendingStop}
        title="Desconectar canal"
        message="A sessão será encerrada e o canal para de receber mensagens até ser reconectado."
        confirmLabel="Desconectar"
        destructive
        isPending={stop.isPending}
        onConfirm={() => pendingStop && stop.mutate(pendingStop.id)}
        onCancel={() => setPendingStop(null)}
      />
    </div>
  )
}

/**
 * List() (backend) never reconciles live WAHA session state — only Get(id) does. This
 * wrapper reconciles each row against the server the same way the wizard already does
 * (useLiveConnection, existing polling contract), so a card correctly reflects
 * qr_required/connected/disconnected instead of staying stuck on whatever List()
 * returned when the page loaded. The base list query remains the source of which
 * connections exist; this only refines their live state — status authority stays
 * server-side (toConnectionState never runs on a frontend timer/assumption).
 */
function LiveChannelConnectionCard({
  connection,
  provider,
  onTest,
  onStart,
  onRequestStop,
  testPending,
  startPending,
}: {
  connection: ChannelConnection
  provider?: ProviderDescriptor
  onTest: (id: string) => void
  onStart: (id: string) => void
  onRequestStop: (c: ChannelConnection) => void
  testPending: boolean
  startPending: boolean
}) {
  const navigate = useNavigate()
  const { connection: live, state } = useLiveConnection(connection)
  const isLive = state === 'connected' || state === 'degraded'

  const actions: MenuAction[] = [
    { label: 'Testar conexão', onSelect: () => onTest(live.id), disabled: testPending },
    {
      label: state === 'connected' ? 'Reiniciar sessão' : 'Reconectar',
      onSelect: () => onStart(live.id),
      disabled: startPending,
    },
    {
      label: 'Desconectar',
      onSelect: () => onRequestStop(live),
      destructive: true,
      disabled: !isLive && state !== 'qr_required' && state !== 'connecting',
    },
  ]

  return (
    <ChannelConnectionCard
      connection={live}
      provider={provider}
      actions={actions}
      onOpenDetails={state === 'qr_required' ? () => navigate(`/channels/whatsapp/new?connection=${live.id}`) : undefined}
    />
  )
}
