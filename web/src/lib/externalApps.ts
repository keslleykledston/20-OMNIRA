import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// Mirrors the administration of external applications (ADR-0048): internal/external/adapters/{admin,webhook_admin}_http.go.
// A token or a signing secret is shown ONCE, in the response that creates it; the API only keeps a digest / a sealed copy.

export const SCOPES: { key: string; label: string; write: boolean }[] = [
  { key: 'conversation.read', label: 'Ler conversas', write: false },
  { key: 'message.read', label: 'Ler mensagens', write: false },
  { key: 'contact.read', label: 'Ler contatos', write: false },
  { key: 'media.read', label: 'Baixar arquivos das mensagens', write: false },
  { key: 'events.read', label: 'Receber o histórico de eventos', write: false },
  { key: 'conversation.claim', label: 'Assumir e devolver conversas', write: true },
  { key: 'message.send', label: 'Enviar mensagens ao cliente', write: true },
  { key: 'conversation.transfer', label: 'Transferir conversas', write: true },
  { key: 'conversation.finalize', label: 'Finalizar atendimentos', write: true },
  { key: 'service.read', label: 'Receber eventos de protocolos, ocorrências, tarefas e ordens de serviço', write: false },
]

export const EVENT_TYPES: { key: string; label: string }[] = [
  { key: 'conversation.created.v1', label: 'Conversa criada' },
  { key: 'conversation.control_changed.v1', label: 'Quem atende mudou' },
  { key: 'conversation.closed.v1', label: 'Atendimento finalizado' },
  { key: 'message.created.v1', label: 'Mensagem nova' },
  { key: 'message.status_changed.v1', label: 'Status de entrega' },
  { key: 'contact.updated.v1', label: 'Contato atualizado' },
  { key: 'ticket.linked.v1', label: 'Chamado vinculado' },
]

// Service Context & Operations (ADR-0049): offered only to an installation that holds the scope service.read (the server refuses the others with 422).
export const SERVICE_EVENT_TYPES: { key: string; label: string }[] = [
  { key: 'service_session.opened.v1', label: 'Protocolo aberto' },
  { key: 'service_session.classified.v1', label: 'Atendimento classificado' },
  { key: 'service_session.status_changed.v1', label: 'Estado do protocolo mudou' },
  { key: 'service_session.closed.v1', label: 'Protocolo encerrado' },
  { key: 'service_session.reopened.v1', label: 'Protocolo reaberto' },
  { key: 'service_case.created.v1', label: 'Ocorrência aberta' },
  { key: 'service_case.updated.v1', label: 'Ocorrência alterada' },
  { key: 'service_case.resolved.v1', label: 'Ocorrência resolvida' },
  { key: 'service_case.assigned.v1', label: 'Ocorrência atribuída' },
  { key: 'service_case.session_linked.v1', label: 'Ocorrência ligada a um protocolo' },
  { key: 'service_task.created.v1', label: 'Tarefa criada' },
  { key: 'service_task.updated.v1', label: 'Tarefa alterada' },
  { key: 'service_task.status_changed.v1', label: 'Estado da tarefa mudou' },
  { key: 'service_task.completed.v1', label: 'Tarefa concluída' },
  { key: 'service_task.assigned.v1', label: 'Tarefa atribuída' },
  { key: 'external_record.linked.v1', label: 'Registro do ERP ligado' },
  { key: 'external_record.unlinked.v1', label: 'Registro do ERP desligado' },
  { key: 'external_record.status_changed.v1', label: 'Registro do ERP mudou de estado' },
  { key: 'external_request.created.v1', label: 'Pedido enviado ao ERP' },
  { key: 'external_request.state_changed.v1', label: 'Pedido ao ERP mudou de estado' },
]

export type AppStatus = 'active' | 'suspended' | 'revoked'

export interface Credential {
  id: string
  key_id: string
  label: string
  created_at: string
  expires_at: string
  revoked_at: string | null
  last_used_at: string | null
  live: boolean
}

export interface ExternalApp {
  id: string
  name: string
  status: AppStatus
  scopes: string[]
  queue_ids: string[]
  channel_ids: string[]
  rate_limit_per_min: number
  lease_seconds: number | null
  allow_outside_hours: boolean
  created_at: string
  updated_at: string
  suspended_at: string | null
  revoked_at: string | null
  credentials?: Credential[]
}

export interface IssuedCredential extends Credential {
  token: string
}

export interface CreateApp {
  name: string
  scopes: string[]
  queue_ids?: string[]
  channel_ids?: string[]
  allow_outside_hours?: boolean
  credential_ttl_days?: number
  credential_label?: string
}

export interface UpdateApp {
  name?: string
  scopes?: string[]
  queue_ids?: string[]
  channel_ids?: string[]
  allow_outside_hours?: boolean
}

export interface Webhook {
  id: string
  url: string
  event_types: string[]
  status: 'active' | 'paused'
  key_id: string
  previous_key_id: string | null
  previous_valid_until: string | null
  enqueued_through_seq: number
  consecutive_failures: number
  last_delivery_at: string | null
  last_http_status: number | null
  last_error_class: string | null
  created_at: string
  updated_at: string
}

export interface Delivery {
  id: string
  event_id: string
  event_seq: number
  status: 'pending' | 'delivered' | 'dead' | 'cancelled'
  attempts: number
  next_attempt_at: string
  last_attempt_at: string | null
  delivered_at: string | null
  last_http_status: number | null
  last_error_class: string | null
  created_at: string
}

const appsBase = () => `${API_BASE}/tenants/${getTenantId()}/integrations/apps`

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

const h = () => ({ headers: authHeaders() })

export const externalAppsAPI = {
  list: () => call<{ items: ExternalApp[]; count: number }>(() => axios.get(appsBase(), h())),
  get: (id: string) => call<ExternalApp>(() => axios.get(`${appsBase()}/${id}`, h())),
  create: (body: CreateApp) => call<{ installation: ExternalApp; credential: IssuedCredential }>(() => axios.post(appsBase(), body, h())),
  update: (id: string, body: UpdateApp) => call<ExternalApp>(() => axios.patch(`${appsBase()}/${id}`, body, h())),
  suspend: (id: string) => call<{ installation: ExternalApp; released_conversations: number } | ExternalApp>(() => axios.post(`${appsBase()}/${id}/suspend`, null, h())),
  reactivate: (id: string) => call<ExternalApp>(() => axios.post(`${appsBase()}/${id}/reactivate`, null, h())),
  revoke: (id: string) => call<{ installation: ExternalApp; released_conversations: number } | ExternalApp>(() => axios.post(`${appsBase()}/${id}/revoke`, null, h())),
  issueCredential: (id: string, body: { label?: string; ttl_days?: number }) => call<IssuedCredential>(() => axios.post(`${appsBase()}/${id}/credentials`, body, h())),
  revokeCredential: (id: string, credentialId: string) => call<unknown>(() => axios.delete(`${appsBase()}/${id}/credentials/${credentialId}`, h())),
  webhooks: {
    list: (id: string) => call<{ items: Webhook[]; count: number }>(() => axios.get(`${appsBase()}/${id}/webhooks`, h())),
    create: (id: string, body: { url: string; event_types: string[] }) => call<{ subscription: Webhook; secret: string }>(() => axios.post(`${appsBase()}/${id}/webhooks`, body, h())),
    update: (id: string, hook: string, body: { url?: string; event_types?: string[]; status?: 'active' | 'paused' }) => call<Webhook>(() => axios.patch(`${appsBase()}/${id}/webhooks/${hook}`, body, h())),
    remove: (id: string, hook: string) => call<unknown>(() => axios.delete(`${appsBase()}/${id}/webhooks/${hook}`, h())),
    rotate: (id: string, hook: string, keepHours?: number) => call<{ key_id: string; secret: string; previous_valid_hours: number }>(() => axios.post(`${appsBase()}/${id}/webhooks/${hook}/rotate-secret`, keepHours ? { keep_hours: keepHours } : {}, h())),
    deliveries: (id: string, hook: string, status?: string) => call<{ items: Delivery[]; count: number }>(() => axios.get(`${appsBase()}/${id}/webhooks/${hook}/deliveries`, { ...h(), params: status ? { status } : undefined })),
    retry: (id: string, hook: string, delivery: string) => call<unknown>(() => axios.post(`${appsBase()}/${id}/webhooks/${hook}/deliveries/${delivery}/retry`, null, h())),
  },
}

const MESSAGES: Record<number, string> = {
  400: 'Dados inválidos. Confira os campos e tente de novo.',
  403: 'Você não tem permissão para gerenciar aplicações externas.',
  404: 'Aplicação não encontrada.',
  409: 'Esta ação não é possível agora (a aplicação foi revogada ou o limite de 5 endpoints foi atingido).',
}

export function externalAppErrorMessage(err: unknown): string {
  const status = (err as { response?: { status?: number } })?.response?.status
  const text = (err as { response?: { data?: unknown } })?.response?.data
  if (status === 400 && typeof text === 'string' && text.trim()) return text.trim()
  if (status && MESSAGES[status]) return MESSAGES[status]
  return 'Não foi possível concluir agora. Tente novamente.'
}

// The address of a webhook is judged by the server (https, a public address, no credentials, port 443 or 8443); this only catches typos early.
export function validateWebhookURL(raw: string): string | null {
  let u: URL
  try {
    u = new URL(raw.trim())
  } catch {
    return 'Informe um endereço completo, por exemplo https://crm.suaempresa.com/omnira.'
  }
  if (u.protocol !== 'https:') return 'O endereço precisa começar com https://.'
  if (u.username || u.password) return 'O endereço não pode ter usuário e senha.'
  if (u.port && u.port !== '443' && u.port !== '8443') return 'Use a porta 443 ou 8443.'
  return null
}

export function statusLabel(s: AppStatus): string {
  return { active: 'Ativa', suspended: 'Suspensa', revoked: 'Revogada' }[s]
}

export function describeError(cls: string | null): string {
  const map: Record<string, string> = {
    timeout: 'o endereço não respondeu a tempo',
    connect: 'não foi possível conectar',
    tls: 'falha no certificado HTTPS',
    blocked_address: 'o endereço aponta para uma rede não permitida',
    http_status: 'o endereço respondeu com erro',
    redirect: 'o endereço tentou redirecionar (não é seguido)',
    not_visible: 'o fato deixou de ser visível para a aplicação',
    revoked: 'a aplicação foi revogada',
    expired: 'o evento saiu da retenção',
  }
  return cls ? map[cls] ?? cls : ''
}
