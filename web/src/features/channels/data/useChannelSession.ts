import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { integrationsAPI, type ChannelConnection } from '../../../lib/integrations'
import { getTenantId } from '../../../lib/session'
import { toConnectionState, type ConnectionState } from '../types'

// The WhatsApp QR rotates every few seconds; the backend always serves the
// current one. These intervals are the ones the pairing flow already shipped
// with — kept here so the wizard and the modal share one lifecycle.
export const QR_REFRESH_MS = 5000
export const STATUS_POLL_MS = 2000

export function connectionKey(id: string) {
  return ['channel-connection', getTenantId(), id]
}

export function qrKey(id: string) {
  return ['channel-qr', getTenantId(), id]
}

export function connectionsKey() {
  return ['channel-connections', getTenantId()]
}

/** Live connection status, polled while the session settles. */
export function useLiveConnection(connection: ChannelConnection) {
  const live = useQuery({
    queryKey: connectionKey(connection.id),
    queryFn: () => integrationsAPI.get(connection.id),
    initialData: connection,
    refetchInterval: STATUS_POLL_MS,
    retry: false,
  })
  const current = live.data ?? connection
  return { connection: current, state: toConnectionState(current) as ConnectionState }
}

/** QR image, polled only while the session actually needs one. */
export function useConnectionQr(connectionId: string, state: ConnectionState) {
  const enabled = state === 'qr_required'
  return useQuery({
    queryKey: qrKey(connectionId),
    queryFn: () => integrationsAPI.qr(connectionId),
    enabled,
    refetchInterval: enabled ? QR_REFRESH_MS : false,
    retry: false,
  })
}

/** (Re)starts the gateway session and refreshes the QR. */
export function useRestartSession(connectionId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => integrationsAPI.start(connectionId),
    onSuccess: (data) => {
      queryClient.setQueryData(connectionKey(connectionId), data)
      void queryClient.invalidateQueries({ queryKey: qrKey(connectionId) })
    },
  })
}

export function useStopSession(connectionId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => integrationsAPI.stop(connectionId),
    onSuccess: (data) => {
      queryClient.setQueryData(connectionKey(connectionId), data)
      void queryClient.invalidateQueries({ queryKey: connectionsKey() })
    },
  })
}

export function useTestConnection(connectionId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => integrationsAPI.test(connectionId),
    onSuccess: (data) => {
      queryClient.setQueryData(connectionKey(connectionId), data)
      void queryClient.invalidateQueries({ queryKey: connectionsKey() })
    },
  })
}
