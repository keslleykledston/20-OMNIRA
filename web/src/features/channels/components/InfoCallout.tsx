import { Icon } from '../../../components/primitives'

/**
 * Explains what the unofficial provider is without alarmist wording and
 * without promising anything about stability or compliance.
 */
export function UnofficialProviderCallout() {
  return (
    <div className="flex gap-3 rounded-card border border-status-info-border bg-status-info-soft p-4">
      <span className="flex-shrink-0 text-status-info">
        <Icon name="info" size={20} />
      </span>
      <div className="text-body-sm">
        <p className="font-semibold text-text-primary">WhatsApp Não Oficial</p>
        <p className="mt-1 text-text-secondary">
          Este provider utiliza uma sessão vinculada ao WhatsApp. A API oficial da Meta também pode
          ser utilizada para operações que exijam o canal oficial.
        </p>
      </div>
    </div>
  )
}
