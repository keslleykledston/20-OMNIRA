import { useEffect, useState } from 'react'
import { getTenantId } from './session'

export interface AccessData {
  role_key: string
  permissions: string[]
  invitation_delivery_available: boolean
}

/**
 * Hook para acessar role + permissions do usuário no tenant.
 * Fetcha de GET /api/v1/tenants/{tenant_id}/me/access
 * Fonte única de verdade para capabilities no frontend.
 */
export function useAccess(): AccessData | null {
  const [access, setAccess] = useState<AccessData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const tenantId = getTenantId()

  useEffect(() => {
    if (!tenantId) {
      setAccess(null)
      return
    }

    const fetchAccess = async () => {
      try {
        const resp = await fetch(
          `/api/v1/tenants/${tenantId}/me/access`,
          { method: 'GET' }
        )
        if (!resp.ok) {
          throw new Error(`HTTP ${resp.status}`)
        }
        const data = await resp.json()
        setAccess(data)
        setError(null)
      } catch (err) {
        setError(err instanceof Error ? err.message : 'unknown error')
        setAccess(null)
      }
    }

    fetchAccess()
  }, [tenantId])

  if (error) {
    console.warn('[useAccess] failed to fetch access:', error)
  }

  return access
}

/**
 * Helper para verificar se o usuário tem uma permissão.
 * Uso: if (can('audit.read')) { showAuditButton() }
 */
export function useCan() {
  const access = useAccess()
  return (permission: string) => {
    return access?.permissions.includes(permission) ?? false
  }
}
