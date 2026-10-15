import type { ReactNode } from 'react'
import { Badge, Button, DropdownMenu, StatusBadge, type MenuAction } from '../primitives'
import type { Availability } from '../../lib/sectors'
import type { QueueMode } from '../../lib/queues'
import { pluralize, type QueueRow } from '../../lib/peopleModel'
import { MODE_LABEL } from './queueText'

function when(iso: string | undefined, tz?: string): string {
  if (!iso) return ''
  try {
    return new Date(iso).toLocaleString('pt-BR', { weekday: 'short', day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: tz || undefined })
  } catch {
    return new Date(iso).toLocaleString('pt-BR')
  }
}

export function OpenBadge({ a }: { a?: Availability }) {
  if (!a) return <Badge size="sm">verificando…</Badge>
  if (a.open) return <StatusBadge status="success" size="sm">Aberto agora{a.next_close ? ` · fecha ${when(a.next_close, a.timezone)}` : ''}</StatusBadge>
  return <StatusBadge status="warning" size="sm">Fechado{a.next_open ? ` · abre ${when(a.next_open, a.timezone)}` : ''}</StatusBadge>
}

export interface QueueItemActions {
  onPeople: () => void
  onNotices: () => void
  onRename: () => void
  onMode: (mode: QueueMode) => void
  onMakeDefault: () => void
  onDelete: () => void
}

export function QueueItem({ queue, availability, manage, busy, sectorControl, ...a }: QueueItemActions & {
  queue: QueueRow
  availability?: Availability
  manage: boolean
  busy: boolean
  sectorControl?: ReactNode
}) {
  const menu: MenuAction[] = [
    { label: 'Renomear', onSelect: a.onRename, disabled: busy },
    ...(['manual', 'round_robin', 'least_loaded'] as const).map((m) => ({
      label: `Estratégia: ${MODE_LABEL[m]}${queue.mode === m ? ' (atual)' : ''}`,
      onSelect: () => a.onMode(m),
      disabled: busy || queue.mode === m,
    })),
    ...(queue.isDefault ? [] : [{ label: 'Definir como fila padrão', onSelect: a.onMakeDefault, disabled: busy }]),
    { label: queue.isDefault ? 'Excluir (troque a padrão antes)' : 'Excluir', onSelect: a.onDelete, destructive: true, disabled: busy || queue.isDefault },
  ]
  const nobody = queue.memberCount === 0
  const noneAvailable = !nobody && queue.availableCount === 0
  return (
    <li className="flex flex-wrap items-start justify-between gap-3 py-3">
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-semibold text-text-primary">{queue.name}</span>
          {queue.isDefault && <Badge size="sm" variant="info">Padrão</Badge>}
          <Badge size="sm">{MODE_LABEL[queue.mode]}</Badge>
          <OpenBadge a={availability} />
        </div>
        <p className="text-xs text-text-secondary">
          {pluralize(queue.memberCount, 'pessoa', 'pessoas')} · {pluralize(queue.availableCount, 'disponível', 'disponíveis')} ·{' '}
          {pluralize(queue.openConversationCount, 'conversa aberta', 'conversas abertas')}
        </p>
        {(nobody || noneAvailable) && (
          <p className="text-xs font-medium text-status-warning-strong">
            {nobody ? 'Ninguém nesta fila: as conversas dela vão esperar.' : 'Ninguém disponível nesta fila agora.'}
          </p>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="secondary" onClick={a.onPeople} aria-label={`Pessoas da fila ${queue.name}`}>Pessoas</Button>
        {manage && <Button size="sm" variant="secondary" onClick={a.onNotices}>Avisos de horário</Button>}
        {sectorControl}
        {manage && <DropdownMenu label={`Mais ações da fila ${queue.name}`} actions={menu} />}
      </div>
    </li>
  )
}
