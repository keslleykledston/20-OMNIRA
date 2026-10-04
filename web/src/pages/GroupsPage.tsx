import { useState } from 'react'
import clsx from 'clsx'
import { useQuery } from '@tanstack/react-query'
import { Button, Icon, PermissionState } from '../components/primitives'
import { GroupList } from '../components/groups/GroupList'
import { GroupThread } from '../components/groups/GroupThread'
import { ManageGroupsModal } from '../components/groups/ManageGroupsModal'
import { groupErrorMessage, groupsAPI } from '../lib/groups'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'

// WhatsApp groups an administrator chose to read (ADR-0015). A separate area from Conversas on purpose:
// a group has no assignee, queue, waiting time or ticket, and nothing can be sent to it from here.
export default function GroupsPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const canRead = access.can('group.read')
  const canManage = access.can('group.manage')
  const accessReady = !access.isLoading
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [managing, setManaging] = useState(false)

  const groups = useQuery({
    queryKey: ['groups-list', tenantId],
    queryFn: groupsAPI.list,
    enabled: canRead,
    retry: false,
    refetchInterval: 30_000,
  })

  if (!access.isLoading && !canRead) {
    return (
      <div className="px-6 py-6 lg:px-8 lg:py-8">
        <PermissionState message="Você não tem permissão para ver os grupos." />
      </div>
    )
  }

  const items = groups.data ?? []
  const selected = items.find((g) => g.id === selectedId) ?? null

  return (
    <div className="h-[calc(100vh-theme(spacing.16))]">
      <div className="grid h-full grid-cols-1 lg:grid-cols-3">
        <div className={clsx('flex-col overflow-hidden lg:flex lg:border-r lg:border-border-subtle', selected ? 'hidden' : 'flex')}>
          <div className="flex items-center justify-between gap-2 border-b border-border-subtle px-4 py-3">
            <div>
              <h2 className="text-base font-semibold text-text-primary">Grupos</h2>
              <p className="text-xs text-text-secondary">Leitura dos grupos habilitados</p>
            </div>
            {canManage && (
              <Button size="sm" variant="secondary" onClick={() => setManaging(true)}>
                Gerenciar grupos
              </Button>
            )}
          </div>
          <div className="flex-1 overflow-y-auto">
            {groups.isError ? (
              <p role="alert" className="p-4 text-sm text-status-danger">
                {groupErrorMessage(groups.error)}
              </p>
            ) : !groups.isLoading && items.length === 0 ? (
              <div className="p-6 text-center">
                <Icon name="whatsapp" size={28} className="mx-auto text-text-tertiary" />
                <p className="mt-3 text-sm font-medium text-text-primary">Nenhum grupo habilitado</p>
                {/* Wait for the permissions before saying who chooses: an administrator must not see "an administrator chooses". */}
                {accessReady && (
                  <p className="mt-1 text-xs text-text-secondary">
                    {canManage
                      ? 'Escolha em "Gerenciar grupos" quais grupos do WhatsApp o OMNIRA deve ler.'
                      : 'Um administrador escolhe quais grupos do WhatsApp são lidos.'}
                  </p>
                )}
              </div>
            ) : (
              <GroupList groups={items} selectedId={selectedId} onSelect={setSelectedId} isLoading={groups.isLoading} />
            )}
          </div>
        </div>

        <div className={clsx('min-h-0 lg:col-span-2 lg:block', selected ? 'block' : 'hidden')}>
          {selected ? (
            <GroupThread key={selected.id} group={selected} canManage={canManage} onBack={() => setSelectedId(null)} />
          ) : (
            <div className="flex h-full items-center justify-center text-sm text-text-secondary">Selecione um grupo</div>
          )}
        </div>
      </div>

      {canManage && <ManageGroupsModal open={managing} onClose={() => setManaging(false)} />}
    </div>
  )
}
