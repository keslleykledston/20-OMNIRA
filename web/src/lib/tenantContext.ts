import { getActingName, isActing } from './acting'
import { useMyTenants } from '../hooks/useMyTenants'
import { getTenantId } from './session'
import { useAuthStore } from './store'
import { tenantDisplayName } from './tenants'

export interface TenantDisplay {
  tenantId: string
  tenantName: string
  roleLabel: string
  /** true while tenantName comes from the placeholder below instead of the backend */
  isPlaceholderName: boolean
}

const ROLE_LABELS: Record<string, string> = {
  admin: 'Administrador',
  tenant_admin: 'Administrador',
  supervisor: 'Supervisor',
  tenant_supervisor: 'Supervisor',
  agent: 'Agente',
  tenant_agent: 'Agente',
}

// Shown only until GET /tenants (hooks/useMyTenants) has answered.
const PLACEHOLDER_TENANT_NAME = 'Tenant ativo'

export function useTenantDisplay(): TenantDisplay {
  const user = useAuthStore((s) => s.user)
  const tenantId = getTenantId()
  const role = user?.roles?.[0] ?? ''
  const mine = useMyTenants().data
  const current = mine?.find((t) => t.id === tenantId)
  // Attending an instance through the Hub: it is not one of the person's own, so the name comes from the context they entered it with.
  if (isActing()) {
    return { tenantId, tenantName: getActingName() || PLACEHOLDER_TENANT_NAME, roleLabel: 'Atendendo pelo Hub', isPlaceholderName: !getActingName() }
  }

  return {
    tenantId,
    // The real name once GET /tenants answers; the placeholder only until then.
    tenantName: current ? tenantDisplayName(current) : PLACEHOLDER_TENANT_NAME,
    roleLabel: ROLE_LABELS[role] ?? role ?? '',
    isPlaceholderName: !current,
  }
}
