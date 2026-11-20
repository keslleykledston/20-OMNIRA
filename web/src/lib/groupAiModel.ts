import {
  GROUP_IMPORT_MAX_BYTES,
  type DiscardReason,
  type EditProposalRequest,
  type GroupImport,
  type ImportStatus,
  type MemoryItem,
  type MemoryKind,
  type MemorySource,
  type MemoryState,
  type MemoryStatus,
  type Proposal,
  type ProposalKind,
  type ProposalPriority,
  type ProposalStatus,
  type TruthLevel,
} from './groupAi'

// Pure helpers of the group copilot screens (ADR-0060): labels, filters, and the wording of the coverage record.
// Nothing here talks to the network.

export const KIND_LABEL: Record<MemoryKind, string> = {
  request: 'Pedido',
  handled_topic: 'Assunto tratado',
  fact: 'Informação',
  agreement: 'Combinado',
  participant_role: 'Papel de participante',
}
export const KINDS = Object.keys(KIND_LABEL) as MemoryKind[]

export const STATUS_LABEL: Record<MemoryStatus, string> = { open: 'Aberto', resolved: 'Resolvido', superseded: 'Substituído' }

export const TRUTH_LABEL: Record<TruthLevel, string> = {
  human_confirmed: 'Confirmado por pessoa',
  ai_inferred: 'Inferido pela IA',
  said_in_group: 'Dito no grupo',
}

export const SOURCE_LABEL: Record<MemorySource, string> = {
  live: 'mensagens do grupo',
  import: 'arquivo importado',
  human: 'registrado por pessoa',
  consolidation: 'condensação da IA',
}

export const IMPORT_STATUS_LABEL: Record<ImportStatus, string> = {
  uploaded: 'Recebido',
  extracting: 'Lendo',
  extracted: 'Lido',
  failed: 'Falhou',
  discarded: 'Original descartado',
}

export const DISCARD_REASON_LABEL: Record<DiscardReason, string> = {
  confirmed: 'descarte confirmado por uma pessoa',
  expired: 'prazo vencido',
  history_deleted: 'histórico do grupo apagado',
  group_ai_disabled: 'IA do grupo desligada',
  memory_erased: 'memória do grupo apagada',
}

const REJECTED_REASON_LABEL: Record<string, string> = {
  system: 'linhas do sistema',
  deleted: 'mensagens apagadas',
  empty: 'vazias',
  bad_date: 'data não reconhecida',
}

export const PROPOSAL_KIND_LABEL: Record<ProposalKind, string> = { case: 'Ocorrência', task: 'Tarefa' }
export const PROPOSAL_STATUS_LABEL: Record<ProposalStatus, string> = {
  pending_approval: 'Aguardando aprovação',
  created: 'Criada',
  rejected: 'Rejeitada',
  failed: 'Falhou',
}
export const PRIORITY_LABEL: Record<ProposalPriority, string> = { low: 'Baixa', normal: 'Normal', high: 'Alta', urgent: 'Urgente' }
export const PRIORITIES = Object.keys(PRIORITY_LABEL) as ProposalPriority[]

// ---- formatting

export function formatWhen(iso?: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function formatDay(iso?: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleDateString('pt-BR', { day: '2-digit', month: '2-digit', year: 'numeric' })
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1).replace('.', ',')} KiB`
  return `${(n / (1024 * 1024)).toFixed(1).replace('.', ',')} MiB`
}

/** Short form of an id, for confirmations only ("registro 1a2b3c4d"). */
export function shortId(id: string): string {
  return id.slice(0, 8)
}

/** <input type="datetime-local"> value (local time, no zone) from an ISO instant; '' when none. */
export function toLocalInput(iso?: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

/** The ISO instant of a datetime-local value; null for empty (clears the due date). */
export function fromLocalInput(value: string): string | null {
  if (!value) return null
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? null : d.toISOString()
}

// ---- memory

export type StatusFilter = 'active' | MemoryStatus

/** "active" = open + resolved (the server leaves superseded out unless asked). A kind of '' = all kinds. */
export function filterMemory(items: MemoryItem[], f: { kind: MemoryKind | ''; status: StatusFilter }): MemoryItem[] {
  return items.filter((i) => {
    if (f.kind && i.kind !== f.kind) return false
    if (f.status === 'active') return i.status !== 'superseded'
    return i.status === f.status
  })
}

/** How many requests are still open: what a person looking at the group wants to know first. */
export function openRequestCount(items: MemoryItem[]): number {
  return items.filter((i) => i.kind === 'request' && i.status === 'open').length
}

const at = (iso?: string | null) => (iso ? new Date(iso).getTime() : NaN)

/** A "read now" was asked and the worker has not finished a run since: the request is still waiting. */
export function analysisPending(s?: MemoryState): boolean {
  if (!s?.analyze_requested_at) return false
  const asked = at(s.analyze_requested_at)
  const ran = at(s.last_run_at)
  return Number.isNaN(ran) || asked > ran
}

export function consolidationPending(s?: MemoryState): boolean {
  if (!s?.consolidate_requested_at) return false
  const asked = at(s.consolidate_requested_at)
  const done = at(s.last_consolidated_at)
  return Number.isNaN(done) || asked > done
}

/** What the "last reading" line says. Honest about never having run and about a failed run. */
export function lastRunText(s?: MemoryState): string {
  if (!s?.last_run_at) return 'A IA ainda não leu este grupo.'
  const when = formatWhen(s.last_run_at)
  if (s.last_run_ok === false) return `Última leitura em ${when}: falhou. Os itens abaixo são os de antes dela.`
  return `Última leitura em ${when}.`
}

export function evidenceLine(item: Pick<MemoryItem, 'evidence' | 'tier'>): string {
  if (item.evidence.length === 0) {
    return item.tier === 'consolidated' ? 'Conhecimento condensado: não guarda trecho literal.' : 'Sem trecho guardado.'
  }
  return `${item.evidence.length} ${item.evidence.length === 1 ? 'trecho' : 'trechos'} de evidência`
}

// ---- imports

export function importInProgress(i: Pick<GroupImport, 'status'>): boolean {
  return i.status === 'uploaded' || i.status === 'extracting'
}

/** Discarding the original is a person's confirmation and the server only allows it when everything was READ. */
export function canDiscardImport(i: Pick<GroupImport, 'raw_available' | 'coverage_complete'>): boolean {
  return i.raw_available && i.coverage_complete
}

export function rejectedReasonsText(reasons?: Record<string, number>): string {
  if (!reasons) return ''
  return Object.entries(reasons)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => `${n} ${REJECTED_REASON_LABEL[k] ?? k}`)
    .join(', ')
}

export interface CoverageVerdict {
  tone: 'success' | 'warning' | 'info' | 'danger'
  title: string
  detail: string
}

/**
 * The sentence about coverage. coverage_complete means every message went through a reading batch and none failed: "tudo foi lido".
 * It never says everything was captured, because the AI only records what looks relevant.
 */
export function coverageVerdict(i: GroupImport): CoverageVerdict {
  if (importInProgress(i)) {
    return { tone: 'info', title: 'Leitura em andamento', detail: 'A IA ainda está lendo o arquivo. O resultado aparece aqui sozinho.' }
  }
  if (i.status === 'failed') {
    return { tone: 'danger', title: 'A leitura falhou', detail: 'O arquivo não foi lido por completo. Se o original ainda existe, dá para reprocessar.' }
  }
  if (i.coverage_complete) {
    return {
      tone: 'success',
      title: 'Todas as mensagens foram lidas',
      detail: 'Cobertura completa quer dizer que tudo foi lido, não que tudo foi registrado: a IA guarda só o que parece relevante.',
    }
  }
  const failed = i.batches_failed ?? 0
  return {
    tone: 'warning',
    title: 'Cobertura incompleta',
    detail:
      failed > 0
        ? `${failed} ${failed === 1 ? 'lote falhou' : 'lotes falharam'}: parte das mensagens não foi lida. Reprocessar tenta de novo.`
        : 'Nem todas as mensagens foram lidas ainda.',
  }
}

/** Why the original cannot be discarded by hand yet, or '' when it can (or is already gone). */
export function discardHint(i: GroupImport): string {
  if (!i.raw_available || i.coverage_complete) return ''
  return `Só dá para descartar depois que todas as mensagens forem lidas. Se isso não acontecer, o arquivo é descartado sozinho em ${formatDay(i.expires_at)}.`
}

/** Client-side check before sending (the server checks again: size, antivirus, format). */
export function validateImportFile(file: { name: string; size: number }): string | null {
  if (!/\.(txt|zip)$/i.test(file.name)) return 'Envie a exportação da conversa do WhatsApp: um arquivo .txt ou .zip.'
  if (file.size === 0) return 'O arquivo está vazio.'
  if (file.size > GROUP_IMPORT_MAX_BYTES) return 'O arquivo passa de 50 MiB.'
  return null
}

// ---- proposals

/** Only what changed, plus the version the screen read (the server answers 409 when it is stale). */
export function proposalPatch(
  original: Proposal,
  draft: { title: string; description: string; priority: ProposalPriority; due: string },
): EditProposalRequest {
  const out: EditProposalRequest = { version: original.version }
  if (draft.title.trim() !== original.title) out.title = draft.title.trim()
  if (draft.description.trim() !== original.description) out.description = draft.description.trim()
  if (draft.priority !== original.priority) out.priority = draft.priority
  const due = fromLocalInput(draft.due)
  if ((due ?? null) !== (original.due_at ?? null)) out.due_at = due
  return out
}

export function proposalChanged(patch: EditProposalRequest): boolean {
  return Object.keys(patch).some((k) => k !== 'version')
}

export function proposalOutcome(p: Pick<Proposal, 'kind' | 'status' | 'created_case_id' | 'created_task_id'>): string {
  if (p.status !== 'created') return ''
  if (p.kind === 'case') return p.created_case_id ? `Ocorrência criada (registro ${shortId(p.created_case_id)}).` : 'Ocorrência criada.'
  return p.created_task_id ? `Tarefa criada (registro ${shortId(p.created_task_id)}).` : 'Tarefa criada.'
}
