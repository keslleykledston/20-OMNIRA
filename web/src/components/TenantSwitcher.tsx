import { useMyTenants } from '../hooks/useMyTenants'
import { getTenantId } from '../lib/session'
import { switchTenant, tenantDisplayName } from '../lib/tenants'

// Only for people who belong to more than one tenant (a BPO operator, a K3G
// administrator serving several customers); everyone else sees nothing.
export default function TenantSwitcher({ go }: { go?: (path: string) => void }) {
  const tenants = useMyTenants()
  const list = tenants.data ?? []
  if (list.length < 2) return null
  const current = getTenantId()

  return (
    <label className="flex items-center gap-2 text-sm text-text-secondary">
      <span className="hidden sm:inline">Instância</span>
      <select
        aria-label="Trocar de instância"
        value={list.some((t) => t.id === current) ? current : ''}
        onChange={(e) => e.target.value && e.target.value !== current && switchTenant(e.target.value, go)}
        className="h-9 max-w-[8.5rem] truncate rounded-control border border-border-light bg-surface px-2 text-sm font-medium text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary sm:max-w-[16rem]"
      >
        {!list.some((t) => t.id === current) && <option value="" disabled>Selecione…</option>}
        {list.map((t) => (
          <option key={t.id} value={t.id}>
            {tenantDisplayName(t)}
          </option>
        ))}
      </select>
    </label>
  )
}
