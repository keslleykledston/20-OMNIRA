import { useQuery } from '@tanstack/react-query'
import { tenantsAPI } from '../lib/tenants'

// The tenants of the signed-in user. Shared by the switcher and the tenant card.
export function useMyTenants() {
  return useQuery({ queryKey: ['my-tenants'], queryFn: tenantsAPI.mine, staleTime: 5 * 60_000, retry: false })
}
