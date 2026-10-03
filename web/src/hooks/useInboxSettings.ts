import { useQuery } from '@tanstack/react-query'
import { getTenantId } from '../lib/session'
import { DEFAULT_INBOX_SETTINGS, inboxSettingsAPI, toThresholds } from '../lib/inboxSettings'

/** The tenant's wait thresholds; the standard 30 min / 2 h while loading or if the read fails (display only). */
export function useInboxSettings() {
  const tenantId = getTenantId()
  const query = useQuery({
    queryKey: ['inbox-settings', tenantId],
    queryFn: inboxSettingsAPI.get,
    enabled: !!tenantId,
    retry: false,
    staleTime: 5 * 60_000,
  })
  const settings = query.data ?? DEFAULT_INBOX_SETTINGS
  return { settings, thresholds: toThresholds(settings), isLoading: query.isLoading, isError: query.isError }
}
