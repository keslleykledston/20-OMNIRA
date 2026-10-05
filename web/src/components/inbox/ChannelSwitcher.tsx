import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import clsx from 'clsx'
import { getTenantId } from '../../lib/session'
import { openConversationOnChannel, openErrorMessage, useChannelLines, type ChannelLine } from '../../lib/channelLines'
import { ChannelBadge } from './ChannelBadge'

interface Props {
  contactId: string
  /** The line the open conversation is on. */
  currentChannelId?: string
  /** Called with the conversation to show after opening/finding it on the chosen line. */
  onOpenConversation: (conversationId: string) => void
}

/**
 * "Canal" section of the context pane: which line this conversation runs on, and the other lines this person can be
 * reached through. Choosing one opens (or finds) the person's separate conversation on that line; it sends nothing.
 * A conversation never moves between lines: its history stays where it happened.
 */
export function ChannelSwitcher({ contactId, currentChannelId, onOpenConversation }: Props) {
  const lines = useChannelLines()
  const queryClient = useQueryClient()
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const all = lines.data ?? []
  if (all.length === 0) return null
  const current = all.find((l) => l.id === currentChannelId)
  const others = all.filter((l) => l.id !== currentChannelId && l.can_send_text)

  const open = async (line: ChannelLine) => {
    setBusy(line.id)
    setError(null)
    try {
      const res = await openConversationOnChannel(contactId, line.id)
      void queryClient.invalidateQueries({ queryKey: ['inbox-conversations', getTenantId()] })
      onOpenConversation(res.conversation_id)
    } catch (e) {
      setError(openErrorMessage(e))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="p-4 border-b border-border-subtle">
      <h4 className="text-xs font-semibold text-text-tertiary mb-2 uppercase">Canal</h4>
      <p className="mb-2 flex items-center gap-2 text-sm text-text-primary">
        <span className="min-w-0 truncate">{current?.label ?? 'Canal desconhecido'}</span>
        <ChannelBadge line={current} />
      </p>
      {others.length > 0 && (
        <>
          <p className="mb-1.5 text-xs text-text-secondary">Falar com esta pessoa por outro canal:</p>
          <ul className="space-y-1.5">
            {others.map((l) => (
              <li key={l.id}>
                <button
                  type="button"
                  onClick={() => void open(l)}
                  disabled={busy !== null}
                  className={clsx(
                    'flex w-full items-center justify-between gap-2 rounded-control border border-border-subtle bg-surface px-2.5 py-1.5 text-left text-sm',
                    'hover:border-accent-primary hover:bg-accent-primary-soft disabled:opacity-60',
                  )}
                >
                  <span className="min-w-0 truncate">{l.label}</span>
                  <span className="flex-shrink-0 text-xs text-accent-primary">{busy === l.id ? 'Abrindo…' : 'Abrir conversa'}</span>
                </button>
                {l.window_required && (
                  <p className="mt-0.5 px-1 text-[11px] text-text-tertiary">
                    A Meta só aceita texto livre até 24 h depois da última mensagem do cliente neste número.
                  </p>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
      {error && (
        <p role="alert" className="mt-2 text-xs text-status-danger">
          {error}
        </p>
      )}
    </div>
  )
}
