import clsx from 'clsx'
import { Badge, Icon, Modal } from '../../../components/primitives'
import type { ProviderDescriptor } from '../../../lib/integrations'
import { PROVIDER_KIND_LABEL } from '../types'

interface Props {
  open: boolean
  providers: ProviderDescriptor[]
  onSelect: (provider: ProviderDescriptor) => void
  onClose: () => void
}

/**
 * Provider picker. Availability comes from the descriptor (`enabled` /
 * `unavailable_reason`), so a provider without a flow shows why instead of
 * pretending to work.
 */
export function AddChannelDialog({ open, providers, onSelect, onClose }: Props) {
  return (
    <Modal
      open={open}
      title="Adicionar canal"
      description="Escolha o provider da conexão."
      onClose={onClose}
    >
      <ul className="space-y-2">
        {providers.map((p) => (
          <li key={p.id}>
            <button
              type="button"
              disabled={!p.enabled}
              onClick={() => onSelect(p)}
              className={clsx(
                'flex w-full items-start gap-3 rounded-card border p-4 text-left transition-colors',
                'focus-visible:ring-2 focus-visible:ring-accent-primary',
                p.enabled
                  ? 'border-border-subtle hover:border-accent-primary hover:bg-accent-primary-soft'
                  : 'cursor-not-allowed border-border-subtle bg-surface-muted',
              )}
            >
              <span
                className={clsx(
                  'flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-card',
                  p.enabled
                    ? 'bg-status-success-soft text-status-success'
                    : 'bg-surface-muted text-text-tertiary',
                )}
              >
                <Icon name="whatsapp" size={22} />
              </span>

              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-2">
                  <span
                    className={clsx(
                      'text-body-md font-semibold',
                      p.enabled ? 'text-text-primary' : 'text-text-tertiary',
                    )}
                  >
                    {p.name}
                  </span>
                  <Badge variant={p.kind === 'official' ? 'info' : 'default'} size="sm">
                    {PROVIDER_KIND_LABEL[p.kind]}
                  </Badge>
                </span>
                <span className="mt-1 block text-body-sm text-text-secondary">
                  {p.enabled
                    ? p.connect_method === 'qr_session'
                      ? 'Conexão por leitura de QR Code.'
                      : 'Conexão por credenciais do provider.'
                    : (p.unavailable_reason ?? 'Configuração indisponível no momento.')}
                </span>
              </span>
            </button>
          </li>
        ))}
      </ul>
    </Modal>
  )
}
