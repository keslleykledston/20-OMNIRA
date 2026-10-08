import { useQuery } from '@tanstack/react-query'
import { hubAPI } from '../lib/hub'

// The Service Hubs the signed-in user belongs to. Empty when the Hub is not enabled on the server.
export function useMyHubs() {
  return useQuery({ queryKey: ['my-hubs'], queryFn: hubAPI.mine, staleTime: 5 * 60_000, retry: false })
}
