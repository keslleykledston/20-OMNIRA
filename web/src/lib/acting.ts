import axios from 'axios'
import { getTenantId } from './session'

// Delegated serving (ADR-0040). A person who attends an instance only through the Hub works in the SAME workspace as its members, but
// every request for that instance declares the context it acts in: `X-Omnira-Acting-As: hub:<id>`. The header grants nothing; the server proves
// hub -> contract -> grant -> permissions on its own for each request. It is added ONLY to requests for the instance the session is acting in.
export const ACTING_KEY = 'actingHub'
export const ACTING_NAME_KEY = 'actingName'
export const ACTING_HEADER = 'X-Omnira-Acting-As'

function read(key: string): string {
  try {
    return localStorage.getItem(key) || ''
  } catch {
    return ''
  }
}

export const getActingHub = (): string => read(ACTING_KEY)
export const getActingName = (): string => read(ACTING_NAME_KEY)
export const isActing = (): boolean => !!read(ACTING_KEY)

export function setActing(hubId: string, instanceName: string): void {
  try {
    localStorage.setItem(ACTING_KEY, hubId)
    localStorage.setItem(ACTING_NAME_KEY, instanceName)
  } catch {
    // storage blocked: the session simply does not act for a hub
  }
}

export function clearActing(): void {
  try {
    localStorage.removeItem(ACTING_KEY)
    localStorage.removeItem(ACTING_NAME_KEY)
  } catch {
    // nothing to clear
  }
}

/** Where an <img>/<audio>/<video> loads a message's file from. They cannot send the acting header, so the delegated path names the hub in the URL. */
export function messageMediaUrl(tenantId: string, messageId: string): string {
  const hub = getActingHub()
  return hub
    ? `/api/v1/hubs/${hub}/serve/${tenantId}/messages/${messageId}/media`
    : `/api/v1/tenants/${tenantId}/messages/${messageId}/media`
}

let installed = false

/** Adds the acting header to the requests for the instance the session is acting in. Idempotent (hot reload safe). */
export function installActingInterceptor(): void {
  if (installed) return
  installed = true
  axios.interceptors.request.use((config) => {
    const hub = getActingHub()
    if (!hub) return config
    const tenant = getTenantId()
    const url = String(config.url ?? '')
    if (tenant && url.includes(`/tenants/${tenant}/`)) {
      config.headers.set(ACTING_HEADER, `hub:${hub}`)
    }
    return config
  })
}
