import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// Espelha contracts/openapi/flows-v1.yaml (ADR-0019). A autoridade é sempre o backend: a UI só evita oferecer ações que ele
// recusaria e mostra os problemas que ele devolve. Nada aqui executa um fluxo.

export type FlowType = 'INBOUND' | 'INTERNAL' | 'SUBFLOW' | 'EVENT' | 'SURVEY' | 'AFTER_HOURS' | 'AI_WORKFLOW'
export type FlowStatus = 'draft' | 'published' | 'archived'
export type RestartPolicy = 'new_conversation_only' | 'always'

export interface TriggerFilter {
  connection_ids?: string[]
  providers?: string[]
}

export interface Flow {
  id: string
  slug: string
  name: string
  description: string
  type: FlowType
  status: FlowStatus
  draft_revision: number
  active_version: number | null
  priority: number
  is_default: boolean
  trigger_filter: TriggerFilter
  restart_policy: RestartPolicy
  source_template_slug: string | null
  source_template_version: number | null
  created_at: string
  updated_at: string
}

export interface FlowNode {
  id: string
  type: string
  label?: string
  position: { x: number; y: number }
  config?: Record<string, unknown>
}

export interface FlowEdge {
  id: string
  source: string
  sourcePort: string
  target: string
  targetPort?: string
}

export interface FlowVariable {
  name: string
  type?: 'string' | 'number' | 'boolean'
  description?: string
}

/** The contact may end the attendance by typing a command; the bot always asks for a yes first. */
export interface CustomerExit {
  enabled: boolean
  commands?: string[]
  farewell?: string
}

export const DEFAULT_EXIT_COMMANDS = ['encerrar', 'encerrar atendimento', 'sair', '#sair']

export interface FlowDefinition {
  schema_version: 1
  nodes: FlowNode[]
  edges: FlowEdge[]
  variables: FlowVariable[]
  settings: { max_node_executions?: number; input_timeout_seconds?: number; customer_exit?: CustomerExit }
  metadata?: Record<string, unknown>
}

export interface FlowDetail extends Flow {
  definition: FlowDefinition
}

export type IssueSeverity = 'error' | 'warning'

export interface Issue {
  severity: IssueSeverity
  code: string
  node_id?: string
  edge_id?: string
  message: string
}

export interface FlowVersion {
  id: string
  version: number
  definition_hash: string
  note: string
  subflow_pins: Record<string, string>
  published_by: string | null
  published_at: string
  definition?: FlowDefinition
}

export interface NodeTypeInfo {
  type: string
  label: string
  category: string
  side_effect: 'none' | 'local' | 'external'
  waits: boolean
  terminal: boolean
  ports: string[]
}

export interface SimEvent {
  type: 'message' | 'timeout'
  text?: string
}

export interface Scenario {
  contact?: { name?: string; kind?: 'unclassified' | 'customer' | 'other' }
  provider?: 'waha' | 'meta_cloud'
  window_open?: boolean
  companies?: { name: string }[]
  open_tickets?: number
  events?: SimEvent[]
  ai?: { intent?: string; confidence?: number; fail?: boolean }
}

export interface SimulationResult {
  status: string
  steps: { seq: number; node_id: string; node_type: string; status: string; port?: string; error?: string }[]
  messages: { step: number; text: string }[]
  effects: { step: number; kind: string; detail?: Record<string, unknown> }[]
  variables: Record<string, unknown>
  issues?: Issue[]
  error?: string
  events_consumed: number
  waiting_at?: string
}

export interface MappingInfo {
  key: string
  kind: string
  description: string
  required: boolean
}

export interface TemplateInfo {
  slug: string
  version: number
  name: string
  description: string
  type: FlowType
  categories: string[]
  difficulty: 'starter' | 'intermediate'
  recommended_for: string[]
  required_features: string[]
  optional_features: string[]
  mappings: MappingInfo[]
  requires_subflows: string[]
  test_cases: number
  hash: string
  definition?: FlowDefinition
}

export interface PackItemInfo {
  template: string
  name: string
  type: FlowType
  version: number
  optional: boolean
  dependency: boolean
}

export interface PackInfo {
  slug: string
  version: number
  name: string
  description: string
  categories: string[]
  recommended_for: string[]
  items: PackItemInfo[]
  mappings: MappingInfo[]
  required_features: string[]
  optional_features: string[]
}

export interface InstallResult {
  pack_installation_id?: string | null
  flows: { flow_id: string; slug: string; template: string; template_version: number; is_default: boolean; dependency: boolean; issues?: Issue[] }[]
  notes?: string[]
}

export interface RunSummary {
  id: string
  flow_id: string
  flow_slug: string
  flow_name: string
  flow_version: number
  conversation_id: string
  status: string
  current_node_id?: string
  node_executions: number
  error?: string
  started_at: string
  completed_at: string | null
  duration_ms: number | null
}

export interface TimelineStep {
  seq: number
  node_id: string
  node_type: string
  status: string
  port?: string
  error?: string
  started_at: string
  duration_ms: number
}

export interface RunDetail extends RunSummary {
  variables: Record<string, unknown>
  handoff?: { summary?: string; queue_id?: string }
  timeline: TimelineStep[]
}

export interface FlowAnalytics {
  days: number
  runs: number
  by_status: Record<string, number>
  completed: number
  failed: number
  human_handoffs: number
  avg_duration_ms: number | null
  node_errors: { node_id: string; node_type?: string; count: number }[]
  drop_off_by_node: { node_id: string; count: number }[]
}

const base = () => `${API_BASE}/tenants/${getTenantId()}`

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

const h = () => ({ headers: authHeaders() })

export const flowsAPI = {
  list: (params?: { status?: FlowStatus; type?: FlowType }) =>
    call<{ items: Flow[] }>(() => axios.get(`${base()}/flows`, { ...h(), params })).then((r) => r.items),
  create: (body: { slug: string; name: string; description?: string; type?: FlowType }) =>
    call<Flow>(() => axios.post(`${base()}/flows`, body, h())),
  get: (id: string) => call<FlowDetail>(() => axios.get(`${base()}/flows/${id}`, h())),
  saveDraft: (id: string, body: { revision: number; name: string; description?: string; definition: FlowDefinition }) =>
    call<{ flow: Flow; issues: Issue[] }>(() => axios.put(`${base()}/flows/${id}/draft`, body, h())),
  validate: (definition: FlowDefinition) =>
    call<{ valid: boolean; issues: Issue[] }>(() => axios.post(`${base()}/flows/validate`, { definition }, h())),
  validateStored: (id: string) => call<{ valid: boolean; issues: Issue[] }>(() => axios.post(`${base()}/flows/${id}/validate`, {}, h())),
  updateSettings: (id: string, body: { priority: number; is_default: boolean; trigger_filter: TriggerFilter; restart_policy: RestartPolicy }) =>
    call<Flow>(() => axios.patch(`${base()}/flows/${id}/settings`, body, h())),
  simulate: (id: string, body: { definition?: FlowDefinition; scenario: Scenario }) =>
    call<SimulationResult>(() => axios.post(`${base()}/flows/${id}/simulate`, body, h())),
  publish: (id: string, body: { revision: number; note?: string }) =>
    call<{ version: FlowVersion; warnings: Issue[] }>(() => axios.post(`${base()}/flows/${id}/publish`, body, h())),
  versions: (id: string) =>
    call<{ items: FlowVersion[] }>(() => axios.get(`${base()}/flows/${id}/versions`, h())).then((r) => r.items),
  activate: (id: string, version: number) => call<Flow>(() => axios.post(`${base()}/flows/${id}/versions/${version}/activate`, {}, h())),
  archive: (id: string) => call<void>(() => axios.post(`${base()}/flows/${id}/archive`, {}, h())),
  nodeTypes: () => call<{ items: NodeTypeInfo[] }>(() => axios.get(`${base()}/flow-node-types`, h())).then((r) => r.items),
  analytics: (id: string, days = 7) => call<FlowAnalytics>(() => axios.get(`${base()}/flows/${id}/analytics`, { ...h(), params: { days } })),

  templates: (params?: { category?: string; q?: string; recommended_for?: string }) =>
    call<{ items: TemplateInfo[] }>(() => axios.get(`${base()}/flow-templates`, { ...h(), params })).then((r) => r.items),
  template: (slug: string) => call<TemplateInfo>(() => axios.get(`${base()}/flow-templates/${slug}`, h())),
  installTemplate: (slug: string, mappings: Record<string, string>) =>
    call<InstallResult>(() => axios.post(`${base()}/flow-templates/${slug}/install`, { mappings }, h())),
  packs: (recommendedFor?: string) =>
    call<{ items: PackInfo[] }>(() => axios.get(`${base()}/flow-packs`, { ...h(), params: recommendedFor ? { recommended_for: recommendedFor } : undefined })).then((r) => r.items),
  pack: (slug: string, templates?: string[]) =>
    call<PackInfo>(() => axios.get(`${base()}/flow-packs/${slug}`, { ...h(), params: templates ? { templates: templates.join(',') } : undefined })),
  installPack: (slug: string, body: { templates?: string[]; mappings: Record<string, string> }) =>
    call<InstallResult>(() => axios.post(`${base()}/flow-packs/${slug}/install`, body, h())),

  runs: (params?: { flow_id?: string; conversation_id?: string; status?: string; limit?: number }) =>
    call<{ items: RunSummary[] }>(() => axios.get(`${base()}/flow-runs`, { ...h(), params })).then((r) => r.items),
  run: (id: string) => call<RunDetail>(() => axios.get(`${base()}/flow-runs/${id}`, h())),
}

interface ApiErrorBody {
  error?: string
  detail?: string
  issues?: Issue[]
  missing?: string[]
}

export function flowErrorBody(err: unknown): ApiErrorBody {
  return ((err as { response?: { data?: ApiErrorBody } })?.response?.data ?? {}) as ApiErrorBody
}

export function flowErrorStatus(err: unknown): number | undefined {
  return (err as { response?: { status?: number } })?.response?.status
}

// Frases que o operador consegue agir; o corpo cru da API nunca vai para a tela.
export function flowErrorMessage(err: unknown): string {
  const body = flowErrorBody(err)
  switch (body.error) {
    case 'revision_conflict':
      return 'Este rascunho foi alterado por outra pessoa. Recarregue para ver a versão mais recente antes de salvar.'
    case 'slug_taken':
      return 'Já existe um fluxo com este identificador.'
    case 'not_publishable':
      return 'O fluxo tem erros que impedem a publicação. Corrija os itens destacados.'
    case 'missing_mappings':
      return `Falta mapear: ${(body.missing ?? []).join(', ')}.`
    case 'archived':
      return 'Este fluxo está arquivado e não pode mais ser alterado.'
    case 'invalid':
      return body.detail || 'Algum dado informado é inválido.'
    case 'templates_unavailable':
    case 'runs_unavailable':
      return 'Este recurso não está habilitado neste ambiente.'
  }
  switch (flowErrorStatus(err)) {
    case 403:
      return 'Você não tem permissão para esta ação.'
    case 404:
      return 'Não encontrado.'
    case 501:
      return 'Este recurso não está habilitado neste ambiente.'
  }
  return 'Não foi possível concluir a operação. Tente novamente.'
}
