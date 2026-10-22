import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders } from './session'

// Mirrors internal/platformrelease/updatemgr (F6) and its 10 /platform/* routes in
// contracts/openapi/omnira-v1.yaml. Authority is always IsPlatformOperator, decided entirely by
// the server from the session (ADR-0054 §2.2) — this module never sends tenant_id/hub_id, and
// never assumes a 404 means "does not exist": it is the SAME uniform answer for "you are not a
// platform operator" (no enumeration oracle, see updatemgr/http.go `scope`).

export interface ReleaseManifest {
  schema_version: number
  release_id: string
  platform_version: string
  channel: 'candidate' | 'stable' | string
  source: { repository: string; commit_sha: string; workflow_run_id: string }
  artifacts: { unit: string; image: string }[]
  modules_changed: string[]
  compatibility: { from_versions: string[]; min_agent_protocol: number; database_strategy: string }
  migrations: { start_after: string; target: string; checksum_bundle: string }
  rollout: { workloads: string[]; preflight_policy: string; recovery_class: string }
  release_notes_ref?: string
  approved_by: string
}

export interface PlatformRelease {
  release_id: string
  channel: string
  platform_version: string
  manifest: ReleaseManifest
  ingested_by: string
  ingested_at: string
}

export interface PlatformInstallation {
  channel: string
  agent_protocol: number
  current_version?: string
  current_release_id?: string
  current_digests: Record<string, unknown>
  updated_at: string
}

export interface PlatformBackupRecord {
  id: string
  kind: string
  taken_at: string
  verified: boolean
  size_bytes?: number
  sha256_hex?: string
  detail: Record<string, unknown>
  created_by: string
  created_at: string
}

export interface PlatformUpdatePlanDetail {
  version_known: boolean
  version_compatible: boolean
  agent_protocol_ok: boolean
  reasons: string[]
  backup_evidence?: unknown
}

export interface PlatformUpdatePlan {
  id: string
  release_id: string
  preflight_status: 'PASS' | 'BLOCK'
  preflight_detail: PlatformUpdatePlanDetail
  created_by: string
  created_at: string
  expires_at: string
}

// The real domain.RunState values (internal/platformrelease/domain) mirrored from the Update
// Agent's local ledger, plus the two Postgres-only sentinels updatemgr itself writes
// (never real ledger states — see service.go `stateRequested`/`stateDispatchConflict`/
// `stateCompatibilityVoided`).
export type PlatformRunState =
  | 'REQUESTED'
  | 'DISCOVERED' | 'PLANNED' | 'PREFLIGHTING' | 'READY' | 'AWAITING_APPROVAL'
  | 'LOCKED' | 'BACKING_UP' | 'BACKUP_VERIFIED' | 'DOWNLOADING' | 'VERIFYING' | 'PREPARING'
  | 'MIGRATING_EXPAND' | 'DEPLOYING' | 'HEALTH_CHECKING' | 'OBSERVING'
  | 'SUCCEEDED' | 'FAILED' | 'RECOVERING' | 'ROLLED_BACK' | 'RECOVERY_REQUIRED'
  | 'BLOCKED' | 'CANCELLED' | 'DISPATCH_CONFLICT' | 'COMPATIBILITY_VOIDED'
  | string

export interface PlatformUpdateRun {
  id: string
  plan_id: string
  release_id: string
  state: PlatformRunState
  idempotency_key: string
  actor_id: string
  created_at: string
  updated_at: string
}

export interface PlatformUpdateEvent {
  seq: number
  kind: 'run.locked' | 'checkpoint' | string
  detail: Record<string, unknown>
  created_at: string
}

// Terminal states: the run will never move again on its own. Kept in one place because both
// the history list (badge color) and the tracking view (stop polling) need the same answer.
const TERMINAL_STATES = new Set<string>([
  'SUCCEEDED', 'FAILED', 'ROLLED_BACK', 'RECOVERY_REQUIRED', 'BLOCKED', 'CANCELLED',
  'DISPATCH_CONFLICT', 'COMPATIBILITY_VOIDED',
])
export function isTerminalRunState(state: string): boolean {
  return TERMINAL_STATES.has(state)
}

const RUN_STATE_LABEL: Record<string, string> = {
  REQUESTED: 'Solicitada',
  LOCKED: 'Bloqueada para atualização',
  BACKING_UP: 'Fazendo backup',
  BACKUP_VERIFIED: 'Backup verificado',
  DOWNLOADING: 'Baixando artefatos',
  VERIFYING: 'Verificando artefatos',
  PREPARING: 'Preparando',
  MIGRATING_EXPAND: 'Aplicando migrations',
  DEPLOYING: 'Implantando',
  HEALTH_CHECKING: 'Verificando saúde',
  OBSERVING: 'Observando',
  SUCCEEDED: 'Concluída com sucesso',
  FAILED: 'Falhou',
  RECOVERING: 'Recuperando',
  ROLLED_BACK: 'Revertida',
  RECOVERY_REQUIRED: 'Recuperação manual necessária',
  BLOCKED: 'Bloqueada',
  CANCELLED: 'Cancelada',
  DISPATCH_CONFLICT: 'Conflito ao despachar (outra origem criou esta execução)',
  COMPATIBILITY_VOIDED: 'Anulada: deixou de ser compatível antes do despacho',
}
export function runStateLabel(state: string): string {
  return RUN_STATE_LABEL[state] ?? state
}

const IDEMPOTENCY_HEADER = 'Idempotency-Key'
function idemHeaders(idempotencyKey: string): Record<string, string> {
  return { ...authHeaders(), [IDEMPOTENCY_HEADER]: idempotencyKey }
}

export const platformReleasesAPI = {
  list: (before?: string): Promise<PlatformRelease[]> =>
    axios
      .get<{ items: PlatformRelease[] }>(`${API_BASE}/platform/releases`, { headers: authHeaders(), params: { limit: 50, before } })
      .then((r) => r.data.items ?? []),
  ingest: (manifest: ReleaseManifest, idempotencyKey: string): Promise<PlatformRelease> =>
    axios.post<PlatformRelease>(`${API_BASE}/platform/releases`, manifest, { headers: idemHeaders(idempotencyKey) }).then((r) => r.data),
}

export const platformInstallationAPI = {
  get: (): Promise<PlatformInstallation> =>
    axios.get<PlatformInstallation>(`${API_BASE}/platform/installation`, { headers: authHeaders() }).then((r) => r.data),
  setVersion: (version: string, idempotencyKey: string, releaseId?: string): Promise<PlatformInstallation> =>
    axios
      .put<PlatformInstallation>(`${API_BASE}/platform/installation/version`, { version, ...(releaseId ? { release_id: releaseId } : {}) }, { headers: idemHeaders(idempotencyKey) })
      .then((r) => r.data),
}

export const platformBackupAPI = {
  record: (
    body: { kind: string; taken_at: string; verified: boolean; size_bytes?: number; sha256_hex?: string; detail?: Record<string, unknown> },
    idempotencyKey: string,
  ): Promise<PlatformBackupRecord> =>
    axios.post<PlatformBackupRecord>(`${API_BASE}/platform/backups`, body, { headers: idemHeaders(idempotencyKey) }).then((r) => r.data),
}

export const platformPlansAPI = {
  create: (releaseId: string, idempotencyKey: string): Promise<PlatformUpdatePlan> =>
    axios.post<PlatformUpdatePlan>(`${API_BASE}/platform/updates/plans`, { release_id: releaseId }, { headers: idemHeaders(idempotencyKey) }).then((r) => r.data),
  get: (planId: string): Promise<PlatformUpdatePlan> =>
    axios.get<PlatformUpdatePlan>(`${API_BASE}/platform/updates/plans/${encodeURIComponent(planId)}`, { headers: authHeaders() }).then((r) => r.data),
}

export const platformRunsAPI = {
  start: (planId: string, idempotencyKey: string): Promise<PlatformUpdateRun> =>
    axios.post<PlatformUpdateRun>(`${API_BASE}/platform/updates/runs`, { plan_id: planId }, { headers: idemHeaders(idempotencyKey) }).then((r) => r.data),
  list: (before?: string): Promise<PlatformUpdateRun[]> =>
    axios
      .get<{ items: PlatformUpdateRun[] }>(`${API_BASE}/platform/updates/runs`, { headers: authHeaders(), params: { limit: 50, before } })
      .then((r) => r.data.items ?? []),
  get: (runId: string): Promise<PlatformUpdateRun> =>
    axios.get<PlatformUpdateRun>(`${API_BASE}/platform/updates/runs/${encodeURIComponent(runId)}`, { headers: authHeaders() }).then((r) => r.data),
  events: (runId: string, after?: number): Promise<PlatformUpdateEvent[]> =>
    axios
      .get<{ items: PlatformUpdateEvent[] }>(`${API_BASE}/platform/updates/runs/${encodeURIComponent(runId)}/events`, { headers: authHeaders(), params: after !== undefined ? { after } : {} })
      .then((r) => r.data.items ?? []),
}

// The write routes answer with plain text (updatemgr/http.go writeErr), never JSON — same
// convention as describeHubWriteError/describeHubAdminError in lib/hub.ts.
export function describePlatformReleaseError(err: unknown): string {
  const e = err as { response?: { status?: number; data?: unknown } }
  const status = e?.response?.status
  const body = typeof e?.response?.data === 'string' ? e.response.data.trim() : ''
  switch (status) {
    case 404:
      return 'Você não tem permissão de operador de plataforma, ou o recurso não existe.'
    case 400:
      return body || 'Pedido inválido. Recarregue a tela e tente de novo.'
    case 422:
      return body || 'Os dados informados não foram aceitos.'
    case 503:
      return body || 'A assinatura da release não pôde ser verificada. Tente novamente mais tarde.'
    default:
      return 'Não foi possível concluir a ação.'
  }
}
