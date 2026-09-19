import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

// WAHA (unofficial WhatsApp) connection management — admin only (channel.manage).
export interface ChannelConnection {
  id: string;
  provider: string;
  provider_kind: 'official' | 'unofficial';
  status: 'pending' | 'active' | 'degraded' | 'disconnected' | 'failed' | 'revoked';
  session_status?: 'missing' | 'starting' | 'needs_qr' | 'working' | 'stopped' | 'failed';
  external_account_id?: string;
  capabilities: string[];
  risk_acknowledged_at?: string;
  created_at: string;
}

export interface QRImage {
  mimetype: string;
  data: string;
}

const base = () => `${API_BASE}/tenants/${getTenantId()}/channels/waha/connections`;

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data;
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized();
    throw err;
  }
}

export const channelsAPI = {
  list: () => call<{ items: ChannelConnection[] }>(() => axios.get(base(), { headers: authHeaders() })).then((d) => d.items),
  get: (id: string) => call<ChannelConnection>(() => axios.get(`${base()}/${id}`, { headers: authHeaders() })),
  create: () =>
    call<ChannelConnection>(() => axios.post(base(), { risk_acknowledged: true }, { headers: authHeaders() })),
  start: (id: string) =>
    call<ChannelConnection>(() => axios.post(`${base()}/${id}/session/start`, undefined, { headers: authHeaders() })),
  stop: (id: string) =>
    call<ChannelConnection>(() => axios.post(`${base()}/${id}/session/stop`, undefined, { headers: authHeaders() })),
  qr: (id: string) => call<QRImage>(() => axios.get(`${base()}/${id}/qr`, { headers: authHeaders() })),
};

// The backend answers plain-text errors; map status codes to operator-facing messages.
export function channelErrorMessage(err: any, fallback = 'Something went wrong'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Only tenant administrators can manage WhatsApp connections';
    case 404:
      return 'Connection not found';
    case 409:
      return 'No QR code is available yet — wait a moment and try again';
    case 422:
      return 'You must acknowledge the risk of using an unofficial WhatsApp connection';
    case 502:
      return 'The WhatsApp gateway (WAHA) is unavailable or rejected the request';
    case 503:
      return 'WhatsApp connections are not configured on the server (public webhook URL or gateway)';
    default:
      return fallback;
  }
}
