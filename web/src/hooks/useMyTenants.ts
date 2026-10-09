import { useQuery } from '@tanstack/react-query'
import { tenantsAPI } from '../lib/tenants'

// The tenants of the signed-in user. Shared by the switcher and the tenant card.
// refreshMs: pages that must notice a lost membership quickly (the Conversas tabs) ask for a periodic re-read.
export function useMyTenants(refreshMs?: number) {
  return useQuery({ queryKey: ['my-tenants'], queryFn: tenantsAPI.mine, staleTime: 5 * 60_000, retry: false, refetchInterval: refreshMs })
}
