import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

export type InvitationStatus = 'pending' | 'accepted' | 'revoked' | 'expired';

// Espelha Invitation em internal/tenancy/adapters/invitations_http.go.
// invite_url é um PATH relativo (ex.: "/invite/<token>"), sem host — o
// backend não sabe qual origem o navegador está usando. Só vem preenchido
// quando o servidor tem o login de desenvolvimento ativo (mesmo gate do dev
// auth) — nunca em produção.
export interface Invitation {
  id: string;
  email: string;
  role_key: string;
  role_name: string;
  status: InvitationStatus;
  created_by_email: string;
  expires_at: string;
  created_at: string;
  // Última entrega bem-sucedida do e-mail; ausente = ainda não entregue.
  sent_at?: string;
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
  | 'email_unverified'
  | 'not_found';

export interface InvitationStatusResponse {
  status: AcceptStatus;
  tenant_name?: string;
  role_key?: string;
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
  resend: (id: string) =>
    call<Invitation>(() => axios.post(`${teamBase()}/${id}/resend`, {}, { headers: authHeaders() })),
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
      // ADR-0039: someone who already works in another instance is the Hub administrator's to authorize.
      if (typeof err?.response?.data === 'string' && err.response.data.includes('another instance')) {
        return 'Esta pessoa já atua em outra instância. Só o administrador do Hub pode autorizá-la em mais de uma: peça a ele.';
      }
      return 'Não foi possível: a pessoa já é membro ativo ou o convite não está mais pendente.';
    case 410:
      return 'Este convite expirou.';
    case 422:
      return 'Essa função não pode ser atribuída aqui.';
    case 502:
      return 'O convite foi registrado, mas o e-mail não pôde ser entregue. Use "Reenviar convite" na lista.';
    case 503:
      return 'O envio de convites não está configurado neste ambiente.';
    default:
      return fallback;
  }
}
