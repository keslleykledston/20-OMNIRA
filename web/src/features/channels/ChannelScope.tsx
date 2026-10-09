import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { API_BASE } from '../../lib/config'
import { createIntegrationsAPI, integrationsAPI, type IntegrationsAPI } from '../../lib/integrations'
import { getTenantId } from '../../lib/session'

/**
 * WHERE the channels screens are working: the signed-in company's own channels (the default), or one instance reached through the
 * Hub by a person the contract delegates management to (ADR-0038 phase 3). The same screens, wizard and dialogs serve both; only the
 * API client, the cache key and the screens' own addresses change. The scope never chooses authority: the server re-decides every call.
 */
export interface ChannelScopeValue {
  api: IntegrationsAPI
  /** Part of every query key, so two instances never share cached connections. */
  key: string
  /** Where the channels list lives (`/channels` for the company, `/instancias/{hub}/{tenant}/canais` through the Hub). */
  basePath: string
  /** Where "go to the conversations" leads after pairing. */
  inboxPath: string
  /** Set when working on an instance through the Hub: its name, for the headings. */
  instanceName?: string
}

const Ctx = createContext<ChannelScopeValue | null>(null)

export function useChannelScope(): ChannelScopeValue {
  const scoped = useContext(Ctx)
  if (scoped) return scoped
  return { api: integrationsAPI, key: getTenantId() ?? '', basePath: '/channels', inboxPath: '/inbox' }
}

export function HubChannelScope({ hubId, tenantId, instanceName, children }: { hubId: string; tenantId: string; instanceName?: string; children: ReactNode }) {
  const value = useMemo<ChannelScopeValue>(() => ({
    api: createIntegrationsAPI(() => `${API_BASE}/hubs/${encodeURIComponent(hubId)}/instances/${encodeURIComponent(tenantId)}/channels`),
    key: `hub:${hubId}:${tenantId}`,
    basePath: `/instancias/${hubId}/${tenantId}/canais`,
    inboxPath: '/inbox',
    instanceName,
  }), [hubId, tenantId, instanceName])
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}
