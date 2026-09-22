import { useEffect, useRef } from 'react'
import { presenceAPI } from '../lib/presence'

const HEARTBEAT_INTERVAL_MS = 30_000

/**
 * ADR-0010: sends a heartbeat immediately on mount, then every 30s while the
 * component is mounted. A focus/reconnect event fires one heartbeat right
 * away without resetting the 30s cadence — never an extra polling loop.
 * A 403 (no active AgentProfile) is expected and silent: not every
 * authenticated user is an operational agent.
 */
export function usePresenceHeartbeat(enabled: boolean) {
  const inFlight = useRef(false)

  useEffect(() => {
    if (!enabled) return

    const send = () => {
      if (inFlight.current) return
      inFlight.current = true
      presenceAPI.heartbeat().finally(() => {
        inFlight.current = false
      })
    }

    send()
    const interval = setInterval(send, HEARTBEAT_INTERVAL_MS)
    const onFocus = () => send()
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onFocus)

    return () => {
      clearInterval(interval)
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onFocus)
    }
  }, [enabled])
}
