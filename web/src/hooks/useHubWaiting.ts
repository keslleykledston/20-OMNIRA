import { useMemo } from 'react'
import { useQueries } from '@tanstack/react-query'
import { hubAPI, type HubCompanyOption, type HubSummary } from '../lib/hub'

const REFRESH_MS = 60_000

// How many conversations wait for an answer across the instances a person attends through their Hubs: the badge of the "Conversas" menu item. DISPLAY ONLY: the
// number is `companies[].unanswered` as the server counted it under this person's own session (include=company_counts), never a sum of lists in the browser.
// It shares its cache entry with the instance tabs of ConversationsEntry (same key, same request), so a screen showing both asks once. The same instance served by
// two Hubs counts once (the first Hub keeps the number, like the tab), so the same conversations are never added twice.
export function waitingAcross(perHub: (HubCompanyOption[] | undefined)[]): number {
  const seen = new Set<string>()
  let total = 0
  for (const companies of perHub) {
    for (const c of companies ?? []) {
      if (seen.has(c.id)) continue
      seen.add(c.id)
      total += c.unanswered ?? 0
    }
  }
  return total
}

export function useHubWaiting(hubs: HubSummary[], enabled = true): number {
  const probes = useQueries({
    queries: hubs.map((h) => ({
      queryKey: ['hub-companies-probe', h.id],
      enabled,
      queryFn: () => hubAPI.inbox(h.id, undefined, [], 1, undefined, '', 'company_counts'),
      staleTime: 10_000,
      refetchInterval: REFRESH_MS,
      retry: false,
    })),
  })
  const data = probes.map((p) => p.data?.companies)
  const key = JSON.stringify(data.map((d) => (d ?? []).map((c) => [c.id, c.unanswered ?? 0])))
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => waitingAcross(data), [key])
}
