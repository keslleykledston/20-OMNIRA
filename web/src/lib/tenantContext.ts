import { getTenantId } from './session'
import { useAuthStore } from './store'

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

// FR0 temporary fixture: the login response carries a tenant object but only
// tenant.id is persisted (lib/session.ts saveSession), so no name reaches the client.
// Replace with the real value once the session payload stores it.
const PLACEHOLDER_TENANT_NAME = 'Tenant ativo'

export function useTenantDisplay(): TenantDisplay {
  const user = useAuthStore((s) => s.user)
  const tenantId = getTenantId()
  const role = user?.roles?.[0] ?? ''

  return {
    tenantId,
    tenantName: PLACEHOLDER_TENANT_NAME,
    roleLabel: ROLE_LABELS[role] ?? role ?? '',
    isPlaceholderName: true,
  }
}
