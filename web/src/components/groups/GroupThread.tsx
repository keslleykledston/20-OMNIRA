import { useMemo, useState } from 'react'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { Button, ConfirmDialog, Icon } from '../primitives'
import { useThreadScroll } from '../../hooks/useThreadScroll'
import { authorLabel, messageText, sortGroupMessages } from '../../lib/groupModel'
import { groupErrorMessage, groupsAPI, type Group, type GroupMessage } from '../../lib/groups'
import { dayLabel } from '../../lib/inboxModel'
import { getTenantId } from '../../lib/session'
import clsx from 'clsx'

interface Props {
  group: Group
  canManage: boolean
  onBack?: () => void
}

const POLL_MS = 15_000

// A group's stored messages, read-only. Same reading behaviour as a conversation (opens on the
// newest, follows what arrives, keeps position when older messages load) but no composer: nothing
// is ever sent to a group from here. New messages arrive by polling, not by the Inbox event stream,
// so a noisy group cannot make every open Inbox reload.
export function GroupThread({ group, canManage, onBack }: Props) {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const { data, hasNextPage, isFetchingNextPage, fetchNextPage, isLoading } = useInfiniteQuery({
    queryKey: ['group-messages', tenantId, group.id],
    initialPageParam: '' as string,
    queryFn: ({ pageParam }) => groupsAPI.messages(group.id, pageParam || undefined),
    getNextPageParam: (last) => (last.has_more && last.next_cursor ? last.next_cursor : undefined),
    enabled: !!tenantId,
    refetchInterval: POLL_MS,
  })

  const messages: GroupMessage[] = useMemo(() => sortGroupMessages(data?.pages.flatMap((p) => p.items) ?? []), [data])
  const { scrollRef, contentRef, atBottom, onScroll, requestOlder, jumpToBottom } = useThreadScroll({
    threadKey: group.id,
    messages,
    hasMore: !!hasNextPage,
    isFetchingMore: isFetchingNextPage,
    fetchMore: fetchNextPage,
  })

  const deleteHistory = async () => {
    setDeleting(true)
    setError(null)
    try {
      await groupsAPI.deleteHistory(group.id)
      setConfirmDelete(false)
      await queryClient.invalidateQueries({ queryKey: ['group-messages', tenantId, group.id] })
      await queryClient.invalidateQueries({ queryKey: ['groups-list', tenantId] })
    } catch (err) {
      setError(groupErrorMessage(err))
      setConfirmDelete(false)
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div className="flex h-full flex-col bg-surface">
      <div className="flex items-center justify-between gap-3 border-b border-border-subtle px-4 py-2.5">
        <div className="flex min-w-0 items-center gap-3">
          {onBack && (
            <button type="button" onClick={onBack} title="Voltar" className="rounded-control p-1 hover:bg-surface-muted lg:hidden">
              <Icon name="arrow-left" />
            </button>
          )}
          <div className="min-w-0">
            <h3 className="truncate font-semibold text-text-primary">{group.name || 'Grupo sem nome'}</h3>
            <p className="text-xs text-text-secondary">Somente leitura · nada é enviado ao grupo</p>
          </div>
        </div>
        {canManage && (
          <Button size="sm" variant="tertiary" onClick={() => setConfirmDelete(true)} disabled={messages.length === 0 && !hasNextPage}>
            Apagar histórico
          </Button>
        )}
      </div>

      {error && (
        <p role="alert" className="border-b border-border-subtle bg-status-danger-soft px-4 py-2 text-xs text-status-danger">
          {error}
        </p>
      )}

      <div className="relative min-h-0 flex-1">
        <div ref={scrollRef} onScroll={onScroll} className="absolute inset-0 overflow-y-auto">
          <div ref={contentRef} className="flex min-h-full flex-col justify-end gap-0.5 px-3 py-2">
            {hasNextPage && (
              <button
                type="button"
                onClick={requestOlder}
                disabled={isFetchingNextPage}
                className="mx-auto mb-1 rounded-pill bg-surface-muted px-3 py-1 text-xs text-text-secondary hover:bg-surface-tertiary disabled:opacity-60"
              >
                {isFetchingNextPage ? 'Carregando mensagens anteriores...' : 'Carregar mensagens anteriores'}
              </button>
            )}
            {isLoading ? (
              <div className="py-10 text-center text-sm text-text-tertiary">Carregando...</div>
            ) : messages.length === 0 ? (
              <div className="py-10 text-center text-sm text-text-tertiary">Nenhuma mensagem guardada ainda.</div>
            ) : (
              messages.map((m, i) => {
                const prev = i > 0 ? messages[i - 1] : null
                const day = dayLabel(m.sent_at)
                const newDay = !prev || dayLabel(prev.sent_at) !== day
                const newAuthor = newDay || !prev || authorLabel(prev) !== authorLabel(m)
                return (
                  <div key={m.id} className={newAuthor && prev ? 'mt-1.5' : undefined}>
                    {newDay && (
                      <div className="my-2 flex justify-center">
                        <span className="rounded-pill bg-surface-muted px-3 py-0.5 text-[11px] text-text-secondary">{day}</span>
                      </div>
                    )}
                    <div className={clsx('flex', m.from_me ? 'justify-end' : 'justify-start')}>
                      <div
                        className={clsx(
                          'max-w-[75%] rounded-lg px-3 py-1.5 text-sm leading-snug',
                          m.from_me ? 'rounded-br-none bg-accent-primary-soft text-text-primary' : 'rounded-bl-none bg-surface-muted text-text-primary',
                        )}
                      >
                        {newAuthor && !m.from_me && <p className="mb-0.5 text-xs font-semibold text-accent-primary">{authorLabel(m)}</p>}
                        <p className={clsx('break-words whitespace-pre-wrap', !m.body.trim() && 'italic text-text-secondary')}>{messageText(m)}</p>
                        <time className="mt-0.5 block text-right text-[11px] text-text-secondary">
                          {new Date(m.sent_at).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' })}
                        </time>
                      </div>
                    </div>
                  </div>
                )
              })
            )}
          </div>
        </div>
        {!atBottom && (
          <button
            type="button"
            onClick={jumpToBottom}
            aria-label="Ir para a última mensagem"
            className="absolute bottom-3 right-4 flex h-9 w-9 items-center justify-center rounded-full border border-border-subtle bg-surface text-text-secondary shadow-sm hover:bg-surface-muted"
          >
            <Icon name="arrow-left" size={16} className="-rotate-90" />
          </button>
        )}
      </div>

      <div className="border-t border-border-subtle px-4 py-2 text-xs text-text-tertiary">
        As respostas a este grupo não são enviadas pelo OMNIRA. Esta área só lê.
      </div>

      <ConfirmDialog
        open={confirmDelete}
        title="Apagar o histórico deste grupo?"
        message="Todas as mensagens guardadas deste grupo são apagadas do OMNIRA, inclusive do arquivo no disco externo, e não podem ser recuperadas por aqui. Cópias de segurança já feitas ainda podem conter essas mensagens até expirarem (7 dias no servidor, 30 na nuvem e 35 no disco externo). O grupo continua habilitado e as novas mensagens voltam a ser guardadas."
        confirmLabel="Apagar histórico"
        destructive
        isPending={deleting}
        onConfirm={() => void deleteHistory()}
        onCancel={() => setConfirmDelete(false)}
      />
    </div>
  )
}
