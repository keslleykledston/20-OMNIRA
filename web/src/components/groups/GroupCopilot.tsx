import { useCallback, useState } from 'react'
import { Drawer, Icon, Tabs, type TabItem } from '../primitives'
import { isCopilotAbsent, isCopilotHidden } from '../../lib/groupAi'
import { openRequestCount } from '../../lib/groupAiModel'
import { getTenantId } from '../../lib/session'
import { useAccess } from '../../lib/useAccess'
import { useGroupImports, useGroupMemory, useGroupProposals } from '../../lib/useGroupAi'
import { ImportsTab } from './copilot/ImportsTab'
import { MemoryTab } from './copilot/MemoryTab'
import { ProposalsTab } from './copilot/ProposalsTab'
import { SettingsTab } from './copilot/SettingsTab'

type TabId = 'memory' | 'imports' | 'proposals' | 'settings'

interface Props {
  group: { id: string; name: string }
}

// "Copiloto" of an open group (ADR-0060). It exists only when the backend says so: the memory route is the probe, and a 404 (flag off) or a
// 403 (no group.ai.read) renders nothing at all, with no error. Everything the panel offers is checked again by the server.
export function GroupCopilot({ group }: Props) {
  const access = useAccess()
  const canRead = access.can('group.ai.read')
  const [open, setOpen] = useState(false)
  const [tab, setTab] = useState<TabId>('memory')

  const probe = useGroupMemory(group.id, { enabled: canRead })
  // the sections that sit behind their own flag (import, proposals) are asked for only once the panel is open; a 404 removes just that tab
  const imports = useGroupImports(group.id, open && canRead)
  const proposals = useGroupProposals(group.id, open && canRead)

  const close = useCallback(() => {
    // Esc inside a confirmation dialog must close only that dialog, not the whole panel beneath it
    if (document.querySelectorAll('[role="dialog"]').length > 1) return
    setOpen(false)
  }, [])

  if (!canRead || !getTenantId()) return null
  if (probe.isError && isCopilotHidden(probe.error)) return null
  if (!probe.data && !probe.isError) return null // still asking: no flash of a button that may vanish

  const importsAvailable = !(imports.isError && isCopilotAbsent(imports.error))
  const proposalsAvailable = !(proposals.isError && isCopilotAbsent(proposals.error))
  const canManage = access.can('group.ai.manage')
  const aiEnabled = !!probe.data?.group.ai_enabled
  const pending = openRequestCount(probe.data?.items ?? [])
  const pendingProposals = (proposals.data?.items ?? []).filter((p) => p.status === 'pending_approval').length

  const tabs: TabItem<TabId>[] = [
    { id: 'memory', label: 'Memória', count: pending },
    ...(importsAvailable ? [{ id: 'imports' as const, label: 'Importações' }] : []),
    ...(proposalsAvailable ? [{ id: 'proposals' as const, label: 'Propostas', count: pendingProposals }] : []),
    { id: 'settings', label: 'Ajustes' },
  ]
  const current: TabId = tabs.some((t) => t.id === tab) ? tab : 'memory'

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label={pending > 0 ? `Copiloto (${pending} ${pending === 1 ? 'pedido aberto' : 'pedidos abertos'})` : 'Copiloto'}
        className="inline-flex items-center gap-1.5 rounded-control px-3 py-2 text-sm font-medium text-accent-primary transition-colors hover:bg-accent-primary-soft focus-visible:ring-2 focus-visible:ring-accent-primary"
      >
        <Icon name="sparkles" size={16} />
        Copiloto
        {pending > 0 && <span aria-hidden="true" className="rounded-pill bg-accent-primary px-1.5 py-0.5 text-[11px] font-semibold leading-none text-white">{pending > 99 ? '99+' : pending}</span>}
      </button>

      <Drawer open={open} title={`Copiloto do grupo ${group.name || ''}`.trim()} onClose={close} size="lg">
        <div className="flex min-h-full flex-col">
          <header className="px-4 pb-0 pt-4 pr-12">
            <h3 className="truncate text-base font-semibold text-text-primary">Copiloto</h3>
            <p className="truncate text-xs text-text-secondary">{group.name || 'Grupo sem nome'} · só propõe, nada é enviado ao grupo</p>
            <Tabs aria-label="Seções do copiloto" items={tabs} value={current} onChange={setTab} className="mt-2 -mx-2" />
          </header>
          <div className="min-h-0 flex-1">
            {current === 'memory' && (
              <MemoryTab
                groupId={group.id}
                canManage={canManage}
                proposalsAvailable={proposalsAvailable && canManage}
                onOpenSettings={() => setTab('settings')}
                onOpenProposals={() => setTab('proposals')}
              />
            )}
            {current === 'imports' && importsAvailable && (
              <ImportsTab groupId={group.id} canImport={access.can('group.import')} aiEnabled={aiEnabled} onOpenSettings={() => setTab('settings')} />
            )}
            {current === 'proposals' && proposalsAvailable && <ProposalsTab groupId={group.id} canManage={canManage} />}
            {current === 'settings' && (
              <SettingsTab
                groupId={group.id}
                groupName={group.name}
                aiEnabled={aiEnabled}
                canManage={canManage}
                canErase={access.can('group.memory.erase')}
                importsAvailable={importsAvailable}
              />
            )}
          </div>
        </div>
      </Drawer>
    </>
  )
}
