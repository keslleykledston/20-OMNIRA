import { useEffect, useRef } from 'react'
import { API_BASE } from '../lib/config'
import { authHeaders, handleUnauthorized } from '../lib/session'
import { readEvents } from './useRealtimeEvents'
import type { PresenceTransitionEvent } from '../lib/presence'

interface UsePresenceEventsOptions {
  tenantId: string
  enabled: boolean
  onEvent: (event: PresenceTransitionEvent) => void
}

const MAX_BACKOFF_MS = 15000

/**
 * ADR-0010 §11: supervisor consumes an initial GET snapshot (presenceAPI.snapshot)
 * plus this incremental SSE stream of aggregated transitions — never raw
 * heartbeats. Same fetch-streaming approach as useRealtimeEvents (SSE cannot
 * carry an Authorization header).
 */
export function usePresenceEvents({ tenantId, enabled, onEvent }: UsePresenceEventsOptions) {
  const onEventRef = useRef(onEvent)
  useEffect(() => {
    onEventRef.current = onEvent
  })

  useEffect(() => {
    if (!enabled || !tenantId) return
    const url = `${API_BASE}/tenants/${tenantId}/agents/presence/events`
    const controller = new AbortController()
    let stopped = false

    const run = async () => {
      let attempt = 0
      while (!stopped) {
        try {
          const res = await fetch(url, {
            headers: { ...authHeaders(), Accept: 'text/event-stream' },
            signal: controller.signal,
          })
          if (res.status === 401) {
            handleUnauthorized()
            return
          }
          if (!res.ok || !res.body) throw new Error(`presence SSE connection failed (HTTP ${res.status})`)
          attempt = 0
          await readEvents(res.body, (data) => {
            try {
              onEventRef.current(JSON.parse(data) as PresenceTransitionEvent)
            } catch {
              // malformed frame: skip, the next snapshot refetch (page reload) recovers
            }
          })
        } catch {
          if (stopped) return
        }
        if (stopped) return
        await new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, Math.min(1000 * 2 ** attempt, MAX_BACKOFF_MS))
          controller.signal.addEventListener('abort', () => { clearTimeout(timer); resolve() }, { once: true })
        })
        attempt += 1
      }
    }
    void run()

    return () => {
      stopped = true
      controller.abort()
    }
  }, [tenantId, enabled])
}
