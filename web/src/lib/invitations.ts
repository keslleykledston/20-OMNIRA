import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

export type InvitationStatus = 'pending' | 'accepted' | 'revoked' | 'expired';

// Espelha Invitation em internal/tenancy/adapters/invitations_http.go.
// invite_url só vem preenchido quando o servidor tem o login de
// desenvolvimento ativo (mesmo gate do dev auth) — nunca em produção.
export interface Invitation {
  id: string;
  email: string;
  role_key: string;
  role_name: string;
  status: InvitationStatus;
  created_by_email: string;
  expires_at: string;
  created_at: string;
  accepted_at?: string;
  revoked_at?: string;
  invite_url?: string;
}

export type AcceptStatus =
  | 'pending'
  | 'accepted'
  | 'revoked'
  | 'expired'
  | 'wrong_identity'
  | 'not_found';

export interface InvitationStatusResponse {
  status: AcceptStatus;
  tenant_name?: string;
  role_name?: string;
  masked_email?: string;
}

const teamBase = () => `${API_BASE}/tenants/${getTenantId()}/team/invitations`;

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data;
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized();
    throw err;
  }
}

export const invitationsAPI = {
  list: () => call<{ items: Invitation[] }>(() => axios.get(teamBase(), { headers: authHeaders() })).then((d) => d.items),
  create: (email: string, roleKey: string) =>
    call<Invitation>(() => axios.post(teamBase(), { email, role_key: roleKey }, { headers: authHeaders() })),
  revoke: (id: string) =>
    call<void>(() => axios.patch(`${teamBase()}/${id}`, { status: 'revoked' }, { headers: authHeaders() })),
};

// Sem tenant na URL: quem está aceitando pode não ter membership em lugar
// nenhum ainda. Usa apenas o cookie de sessão (authHeaders funciona igual,
// sem exigir tenant).
export const invitationAcceptAPI = {
  status: (token: string) =>
    call<InvitationStatusResponse>(() => axios.get(`${API_BASE}/invitations/${token}/status`, { headers: authHeaders() })),
  accept: (token: string) =>
    call<{ tenant_id: string }>(() => axios.post(`${API_BASE}/invitations/${token}/accept`, {}, { headers: authHeaders() })),
};

export function invitationErrorMessage(err: any, fallback = 'Não foi possível concluir a operação'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Você não tem permissão para gerenciar convites.';
    case 404:
      return 'Convite não encontrado.';
    case 409:
      return 'Já existe um convite pendente para este e-mail.';
    case 410:
      return 'Este convite expirou.';
    case 422:
      return 'Essa função não pode ser atribuída aqui.';
    default:
      return fallback;
  }
}
