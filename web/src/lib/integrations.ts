import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

export type ProviderKind = 'official' | 'unofficial';
export type ConnectMethod = 'qr_session' | 'credentials' | 'oauth_redirect';

export interface ProviderInputDescriptor {
  key: string;
  label: string;
  type: 'text' | 'secret' | 'select' | 'phone';
  required: boolean;
  pattern?: string;
  help?: string;
  example?: string;
  secret: boolean;
  options?: string[];
}

export interface ProviderDisplayDescriptor {
  key: string;
  label: string;
  value_template?: string;
  copyable: boolean;
  sensitive: boolean;
  help?: string;
}

export interface ProviderDescriptor {
  id: string;
  name: string;
  channel: string;
  kind: ProviderKind;
  connect_method: ConnectMethod;
  risk_notice?: string;
  capabilities: string[];
  enabled: boolean;
  unavailable_reason?: string;
  inputs: ProviderInputDescriptor[];
  displays: ProviderDisplayDescriptor[];
}

export interface ChannelConnection {
  id: string;
  provider: string;
  provider_kind: ProviderKind;
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

const channelsBase = () => `${API_BASE}/tenants/${getTenantId()}/channels`;
const connectionsBase = () => `${channelsBase()}/connections`;

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data;
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized();
    throw err;
  }
}

export const integrationsAPI = {
  providers: () => call<{ items: ProviderDescriptor[] }>(() => axios.get(`${channelsBase()}/providers`, { headers: authHeaders() })).then((d) => d.items),
  list: () => call<{ items: ChannelConnection[] }>(() => axios.get(connectionsBase(), { headers: authHeaders() })).then((d) => d.items),
  get: (id: string) => call<ChannelConnection>(() => axios.get(`${connectionsBase()}/${id}`, { headers: authHeaders() })),
  create: (provider: string, inputs: Record<string, string>, riskAcknowledged: boolean) =>
    call<ChannelConnection>(() => axios.post(connectionsBase(), {
      provider,
      inputs,
      risk_acknowledged: riskAcknowledged,
    }, { headers: authHeaders() })),
  start: (id: string) => call<ChannelConnection>(() => axios.post(`${connectionsBase()}/${id}/session/start`, undefined, { headers: authHeaders() })),
  test: (id: string) => call<ChannelConnection>(() => axios.post(`${connectionsBase()}/${id}/test`, undefined, { headers: authHeaders() })),
  stop: (id: string) => call<ChannelConnection>(() => axios.post(`${connectionsBase()}/${id}/session/stop`, undefined, { headers: authHeaders() })),
  qr: (id: string) => call<QRImage>(() => axios.get(`${connectionsBase()}/${id}/qr`, { headers: authHeaders() })),
};

export function integrationErrorMessage(err: any, fallback = 'Não foi possível concluir a operação'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Somente administradores do tenant gerenciam integrações.';
    case 404:
      return 'Integração não encontrada.';
    case 409:
      return 'O QR ainda não está disponível. Aguarde um momento e tente novamente.';
    case 422:
      return 'Confira os dados e aceite o aviso de risco para continuar.';
    case 502:
      return 'O gateway do WhatsApp não respondeu. Tente novamente; se persistir, contate o suporte.';
    case 503:
      return 'Integração indisponível: o servidor não está configurado para este provedor.';
    default:
      return fallback;
  }
}
