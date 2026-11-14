import { useMemo, useState } from 'react'
import clsx from 'clsx'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Icon, IconButton, PermissionState } from '../components/primitives'
import { GroupList } from '../components/groups/GroupList'
import { GroupThread } from '../components/groups/GroupThread'
import { LinkGroupModal } from '../components/groups/LinkGroupModal'
import { ManageGroupsModal } from '../components/groups/ManageGroupsModal'
import { OrganizationBrowser } from '../components/groups/OrganizationBrowser'
import { groupErrorMessage, groupsAPI } from '../lib/groups'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'
import { useMyHubs } from '../hooks/useMyHubs'

// WhatsApp groups an administrator chose to read (ADR-0015). A separate area from Conversas on purpose:
// a group has no assignee, queue, waiting time or ticket, and nothing can be sent to it from here.
export default function GroupsPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const canRead = access.can('group.read')
  const canManage = access.can('group.manage')
  const accessReady = !access.isLoading
  const navigate = useNavigate()
  // Hub access is a separate privilege from the Tenant's own group.manage (ADR-0059 vs ADR-0015):
  // only an operator the server already reports as a Hub agent with the 'groups' scope sees this.
  const hubs = useMyHubs()
  const canOrganizeInHub = (hubs.data ?? []).some((h) => h.can_manage_groups)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [managing, setManaging] = useState(false)
  const [linking, setLinking] = useState(false)
  const [onlyUnlinked, setOnlyUnlinked] = useState(false)
  const [connectionFilter, setConnectionFilter] = useState<string>('all')
  // "Mensagens" (lista única, como sempre foi) ou "Organização" (navegar primeiro por Cliente do CRM,
  // depois pelos grupos desse cliente — ver OrganizationBrowser). Puramente client-side: o painel da
  // direita (GroupThread) é sempre o mesmo, qualquer que seja a visão escolhida.
  const [view, setView] = useState<'mensagens' | 'organizacao'>('mensagens')

  const groups = useQuery({
    queryKey: ['groups-list', tenantId, onlyUnlinked],
    queryFn: () => groupsAPI.list(onlyUnlinked ? 'none' : undefined),
    enabled: canRead,
    retry: false,
    refetchInterval: 30_000,
  })

  // Derived client-side from the (capped, small) enabled-groups list: a tenant running more than one
  // WhatsApp instance at once sees them kept apart here too, not just in "Gerenciar grupos".
  const allItems = groups.data ?? []
  const instances = useMemo(() => {
    const seen = new Map<string, string>()
    for (const g of allItems) if (!seen.has(g.connection_id)) seen.set(g.connection_id, g.connection_account || g.connection_id.slice(0, 8))
    return [...seen.entries()].map(([id, label]) => ({ id, label }))
  }, [allItems])

  if (!access.isLoading && !canRead) {
    return (
      <div className="px-6 py-6 lg:px-8 lg:py-8">
        <PermissionState message="Você não tem permissão para ver os grupos." />
      </div>
    )
  }

  const items = connectionFilter === 'all' ? allItems : allItems.filter((g) => g.connection_id === connectionFilter)
  const selected = items.find((g) => g.id === selectedId) ?? null

  return (
    <div className="h-[calc(100vh-theme(spacing.16))]">
      <div className="grid h-full grid-cols-1 lg:grid-cols-4">
        <div className={clsx('flex-col overflow-hidden lg:flex lg:border-r lg:border-border-subtle', selected ? 'hidden' : 'flex')}>
          <div className="flex items-center justify-between gap-2 border-b border-border-subtle px-4 py-3">
            <div>
              <h2 className="text-base font-semibold text-text-primary">Grupos</h2>
              <p className="text-xs text-text-secondary">Leitura dos grupos habilitados</p>
            </div>
            <div className="flex items-center gap-1">
              {canOrganizeInHub && (
                <IconButton size="sm" variant="tertiary" onClick={() => navigate('/admin/grupos')} title="Organizar no Hub" aria-label="Organizar no Hub">
                  <Icon name="dashboard" size={16} />
                </IconButton>
              )}
              {canManage && (
                <IconButton size="sm" variant="secondary" onClick={() => setLinking(true)} title="Vincular Grupo" aria-label="Vincular Grupo">
                  <Icon name="plus" size={16} />
                </IconButton>
              )}
              {canManage && (
                <IconButton size="sm" variant="secondary" onClick={() => setManaging(true)} title="Gerenciar grupos" aria-label="Gerenciar grupos">
                  <Icon name="settings" size={16} />
                </IconButton>
              )}
            </div>
          </div>
          <div className="flex items-center gap-1 border-b border-border-subtle px-4 py-1.5" role="radiogroup" aria-label="Visão dos grupos">
            {([
              { id: 'mensagens', label: 'Mensagens' },
              { id: 'organizacao', label: 'Organização' },
            ] as const).map((o) => (
              <button
                key={o.id}
                type="button"
                role="radio"
                aria-checked={view === o.id}
                onClick={() => setView(o.id)}
                className={clsx(
                  'rounded-control px-2.5 py-1 text-xs font-medium transition-colors',
                  view === o.id ? 'bg-accent-primary-soft text-accent-primary' : 'text-text-secondary hover:text-text-primary',
                )}
              >
                {o.label}
              </button>
            ))}
          </div>
          {view === 'mensagens' && (
            <div className="space-y-1.5 border-b border-border-subtle px-4 py-2">
              {instances.length > 1 && (
                <label className="flex items-center gap-1.5 text-xs text-text-secondary">
                  Instância
                  <select
                    aria-label="Filtrar por instância"
                    value={connectionFilter}
                    onChange={(e) => setConnectionFilter(e.target.value)}
                    className="flex-1 rounded-control border border-border-subtle bg-surface px-1.5 py-1 text-xs text-text-primary focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary"
                  >
                    <option value="all">Todas</option>
                    {instances.map((i) => (
                      <option key={i.id} value={i.id}>
                        {i.label}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              <label className="flex items-center gap-1.5 text-xs text-text-secondary">
                <input type="checkbox" checked={onlyUnlinked} onChange={(e) => setOnlyUnlinked(e.target.checked)} />
                Só grupos sem cliente vinculado
              </label>
            </div>
          )}
          <div className="min-h-0 flex-1 overflow-y-auto">
            {groups.isError ? (
              <p role="alert" className="p-4 text-sm text-status-danger">
                {groupErrorMessage(groups.error)}
              </p>
            ) : !groups.isLoading && (view === 'organizacao' ? allItems : items).length === 0 ? (
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
            ) : view === 'organizacao' ? (
              <OrganizationBrowser allItems={allItems} selectedId={selectedId} onSelect={setSelectedId} />
            ) : (
              <GroupList
                groups={items}
                selectedId={selectedId}
                onSelect={setSelectedId}
                isLoading={groups.isLoading}
                showInstanceLabel={instances.length > 1}
              />
            )}
          </div>
        </div>

        <div className={clsx('min-h-0 lg:col-span-3 lg:block', selected ? 'block' : 'hidden')}>
          {selected ? (
            <GroupThread key={selected.id} group={selected} canManage={canManage} onBack={() => setSelectedId(null)} />
          ) : (
            <div className="flex h-full items-center justify-center text-sm text-text-secondary">Selecione um grupo</div>
          )}
        </div>
      </div>

      {canManage && <ManageGroupsModal open={managing} onClose={() => setManaging(false)} />}
      {canManage && <LinkGroupModal open={linking} onClose={() => setLinking(false)} onDone={() => setLinking(false)} />}
    </div>
  )
}
