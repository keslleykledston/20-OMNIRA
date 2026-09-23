import { Badge, Button, Card, DropdownMenu, Icon, Skeleton, type MenuAction } from '../../../components/primitives'
import type { ChannelConnection, ProviderDescriptor } from '../../../lib/integrations'
import {
  PROVIDER_KIND_LABEL,
  STATE_LABEL,
  STATE_VARIANT,
  formatAccount,
  toConnectionState,
} from '../types'

interface Props {
  connection: ChannelConnection
  provider?: ProviderDescriptor
  actions: MenuAction[]
  onOpenDetails?: () => void
}

/** Derived from the descriptor's connect method so it works for any provider. */
function connectionSummary(provider?: ProviderDescriptor): string {
  switch (provider?.connect_method) {
    case 'qr_session':
      return 'Conexão por sessão vinculada (QR Code)'
    case 'credentials':
      return 'Conexão pela API oficial do provider'
    case 'oauth_redirect':
      return 'Conexão autorizada pelo provider'
    default:
      return 'Conexão de canal'
  }
}

/**
 * One card for every provider. WAHA and Meta differ by props (kind, labels,
 * available metadata), not by a separate markup tree.
 */
export function ChannelConnectionCard({ connection, provider, actions, onOpenDetails }: Props) {
  const state = toConnectionState(connection)
  const kind = connection.provider_kind
  const account = formatAccount(connection.external_account_id)
  const name = provider?.name ?? connection.provider

  return (
    <Card padding="compact" className="flex items-start gap-4">
      {/* channel hue lives on the icon only; the app accent stays blue */}
      <span className="flex h-11 w-11 flex-shrink-0 items-center justify-center rounded-card bg-status-success-soft text-status-success">
        <Icon name="whatsapp" size={24} />
      </span>

      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-body-lg font-semibold text-text-primary">{name}</h3>
          <Badge variant={kind === 'official' ? 'info' : 'default'} size="sm">
            {PROVIDER_KIND_LABEL[kind]}
          </Badge>
          <Badge variant={STATE_VARIANT[state]} size="sm">
            <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
            {STATE_LABEL[state]}
          </Badge>
        </div>

        <p className="mt-0.5 text-body-sm text-text-secondary">{connectionSummary(provider)}</p>

        <dl className="mt-3 space-y-1 text-body-sm">
          <div className="flex gap-2">
            <dt className="text-text-secondary">Número:</dt>
            <dd className="truncate text-text-primary">{account ?? 'não configurado'}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="text-text-secondary">Sessão:</dt>
            {/* short connection id only — never a session token or credential reference */}
            <dd className="truncate font-mono text-text-primary">{connection.id.slice(0, 8)}…</dd>
          </div>
          <div className="flex gap-2">
            <dt className="text-text-secondary">Criada em:</dt>
            <dd className="truncate text-text-primary">
              {new Date(connection.created_at).toLocaleDateString('pt-BR')}
            </dd>
          </div>
        </dl>
      </div>

      <div className="flex flex-shrink-0 items-center gap-2">
        {/* Only rendered for qr_required: this is the one state that needs the
            operator's action right now, so it gets primary emphasis instead of
            sitting at the same visual weight as the secondary menu actions. */}
        {onOpenDetails && (
          <Button variant="primary" size="sm" onClick={onOpenDetails}>
            Detalhes
          </Button>
        )}
        <DropdownMenu actions={actions} label={`Ações de ${name}`} />
      </div>
    </Card>
  )
}

export function ChannelCardSkeleton() {
  return (
    <Card padding="compact" className="flex items-start gap-4">
      <div className="h-11 w-11 flex-shrink-0 animate-pulse rounded-card bg-surface-muted" />
      <div className="flex-1">
        <Skeleton width="w-48" height="h-5" />
        <div className="mt-3">
          <Skeleton width="w-full" height="h-4" count={2} />
        </div>
      </div>
    </Card>
  )
}
