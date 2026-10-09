import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { type ChannelConnection } from '../../../lib/integrations'
import { getTenantId } from '../../../lib/session'
import { useChannelScope } from '../ChannelScope'
import { toConnectionState, type ConnectionState } from '../types'

// The WhatsApp QR rotates every few seconds; the backend always serves the
// current one. These intervals are the ones the pairing flow already shipped
// with — kept here so the wizard and the modal share one lifecycle.
export const QR_REFRESH_MS = 5000
export const STATUS_POLL_MS = 2000

export function connectionKey(id: string, scopeKey: string = getTenantId() ?? '') {
  return ['channel-connection', scopeKey, id]
}

export function qrKey(id: string, scopeKey: string = getTenantId() ?? '') {
  return ['channel-qr', scopeKey, id]
}

export function connectionsKey(scopeKey: string = getTenantId() ?? '') {
  return ['channel-connections', scopeKey]
}

/** Live connection status, polled while the session settles. */
export function useLiveConnection(connection: ChannelConnection) {
  const { api, key } = useChannelScope()
  const live = useQuery({
    queryKey: connectionKey(connection.id, key),
    queryFn: () => api.get(connection.id),
    initialData: connection,
    refetchInterval: STATUS_POLL_MS,
    retry: false,
  })
  const current = live.data ?? connection
  return { connection: current, state: toConnectionState(current) as ConnectionState }
}

/** QR image, polled only while the session actually needs one. */
export function useConnectionQr(connectionId: string, state: ConnectionState) {
  const { api, key } = useChannelScope()
  const enabled = state === 'qr_required'
  return useQuery({
    queryKey: qrKey(connectionId, key),
    queryFn: () => api.qr(connectionId),
    enabled,
    refetchInterval: enabled ? QR_REFRESH_MS : false,
    retry: false,
  })
}

/** (Re)starts the gateway session and refreshes the QR. */
export function useRestartSession(connectionId: string) {
  const queryClient = useQueryClient()
  const { api, key } = useChannelScope()
  return useMutation({
    mutationFn: () => api.start(connectionId),
    onSuccess: (data) => {
      queryClient.setQueryData(connectionKey(connectionId, key), data)
      void queryClient.invalidateQueries({ queryKey: qrKey(connectionId, key) })
    },
  })
}

export function useStopSession(connectionId: string) {
  const queryClient = useQueryClient()
  const { api, key } = useChannelScope()
  return useMutation({
    mutationFn: () => api.stop(connectionId),
    onSuccess: (data) => {
      queryClient.setQueryData(connectionKey(connectionId, key), data)
      void queryClient.invalidateQueries({ queryKey: connectionsKey(key) })
    },
  })
}

export function useTestConnection(connectionId: string) {
  const queryClient = useQueryClient()
  const { api, key } = useChannelScope()
  return useMutation({
    mutationFn: () => api.test(connectionId),
    onSuccess: (data) => {
      queryClient.setQueryData(connectionKey(connectionId, key), data)
      void queryClient.invalidateQueries({ queryKey: connectionsKey(key) })
    },
  })
}
