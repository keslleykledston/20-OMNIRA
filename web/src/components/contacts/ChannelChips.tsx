import { Badge, Icon } from '../primitives'

// A channel kind as the API reports it (e.g. "whatsapp"). null means the
// conversation has no channel connection.
export function ChannelChip({ channel }: { channel: string | null }) {
  if (channel === null) return <Badge size="sm">Sem canal</Badge>
  if (channel.toLowerCase() === 'whatsapp') {
    return (
      <Badge size="sm" variant="success">
        <Icon name="whatsapp" size={14} />
        WhatsApp
      </Badge>
    )
  }
  return <Badge size="sm">{channel}</Badge>
}

export function ChannelChips({ channels }: { channels: string[] }) {
  if (channels.length === 0) return <span className="text-text-tertiary">—</span>
  return (
    <div className="flex flex-wrap gap-1.5">
      {channels.map((c) => (
        <ChannelChip key={c} channel={c} />
      ))}
    </div>
  )
}
