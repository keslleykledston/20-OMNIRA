import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

export type MembershipStatus = 'active' | 'inactive' | 'revoked';

// Espelha TeamMember em internal/tenancy/adapters/team_http.go. last_login_at
// é omitido quando o usuário nunca completou um login OIDC. É o login da
// identidade global, não presença/atividade no tenant — isso é last_seen do
// agente, que pertence a IAM4 e ainda não existe.
export interface TeamMember {
  membership_id: string;
  user_id: string;
  name: string;
  email: string;
  role_key: string;
  role_name: string;
  status: MembershipStatus;
  created_at: string;
  last_login_at?: string;
}

export interface RoleOption {
  id: string;
  key: string;
  name: string;
}

export interface MyAccess {
  role_key: string;
  permissions: string[];
  // Espelha InvitationsHandler.deliveryAvailable: existe alguma forma de o
  // convidado receber o link (dev auth ativo, ou um provedor de e-mail real
  // configurado). Sem isso, o backend recusa POST .../invitations com 503 —
  // esconder o botão aqui é cortesia, não a defesa.
  invitation_delivery_available: boolean;
}

const teamBase = () => `${API_BASE}/tenants/${getTenantId()}`;

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data;
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized();
    throw err;
  }
}

export const teamAPI = {
  list: () => call<{ items: TeamMember[] }>(() => axios.get(`${teamBase()}/team`, { headers: authHeaders() })).then((d) => d.items),
  roles: () => call<{ items: RoleOption[] }>(() => axios.get(`${teamBase()}/roles`, { headers: authHeaders() })).then((d) => d.items),
  myAccess: () => call<MyAccess>(() => axios.get(`${teamBase()}/me/access`, { headers: authHeaders() })),
  updateRole: (membershipId: string, roleKey: string) =>
    call<TeamMember>(() =>
      axios.patch(`${teamBase()}/team/${membershipId}`, { role_key: roleKey }, { headers: authHeaders() }),
    ),
  updateStatus: (membershipId: string, status: MembershipStatus) =>
    call<TeamMember>(() =>
      axios.patch(`${teamBase()}/team/${membershipId}`, { status }, { headers: authHeaders() }),
    ),
};

export function teamErrorMessage(err: any, fallback = 'Não foi possível concluir a operação'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Você não tem permissão para gerenciar a equipe.';
    case 404:
      return 'Membro não encontrado.';
    case 409:
      return 'O tenant ficaria sem nenhum administrador ativo.';
    case 422:
      return 'Essa função não pode ser atribuída aqui.';
    default:
      return fallback;
  }
}
