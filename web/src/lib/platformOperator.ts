import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders } from './session'

// /platform/operator-status (ADR-0038): whether the signed-in person is a platform operator, on
// its own — independent of every OTHER platform feature's own flag (Update Manager, the SLA alert
// kill switch, Context Intelligence v2...). usePlatformOperator() is the ONLY caller; nothing else
// should probe operator status through a feature-specific endpoint (found 2026-11-15: doing so
// through Update Manager's /platform/installation denied a real operator the whole admin console
// on a host where Update Manager alone happened to be disabled).
export interface PlatformOperatorStatus {
  is_operator: boolean
}

export const platformOperatorAPI = {
  get: (): Promise<PlatformOperatorStatus> =>
    axios.get<PlatformOperatorStatus>(`${API_BASE}/platform/operator-status`, { headers: authHeaders() }).then((r) => r.data),
}
