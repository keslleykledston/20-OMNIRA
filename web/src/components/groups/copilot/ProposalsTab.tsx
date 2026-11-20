import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Button, StatusBadge } from '../../primitives'
import { groupAiAPI, groupAiErrorMessage, type Proposal, type ProposalPriority, type ProposalStatus } from '../../../lib/groupAi'
import {
  PRIORITIES,
  PRIORITY_LABEL,
  PROPOSAL_KIND_LABEL,
  PROPOSAL_STATUS_LABEL,
  formatWhen,
  proposalChanged,
  proposalOutcome,
  proposalPatch,
  toLocalInput,
} from '../../../lib/groupAiModel'
import { showToast } from '../../../lib/toast'
import { groupAiKey, useGroupProposals } from '../../../lib/useGroupAi'
import { MiniButton, Notice, SelectField, fieldClass } from './ui'

interface Props {
  groupId: string
  canManage: boolean
}

const statusTone = { pending_approval: 'warning', created: 'success', rejected: 'default', failed: 'danger' } as const

// What the AI (or a person) drafted from the memory. Nothing exists until a person approves: approval creates the service case or task
// under the approver's own permissions (the server asks for service.create). Nothing is ever sent to the group.
export function ProposalsTab({ groupId, canManage }: Props) {
  const qc = useQueryClient()
  const [filter, setFilter] = useState<ProposalStatus | ''>('')
  const [error, setError] = useState<string | null>(null)
  const proposals = useGroupProposals(groupId, true, filter || undefined)
  const refresh = () => void qc.invalidateQueries({ queryKey: groupAiKey(groupId) })

  const approve = useMutation({
    mutationFn: (p: Proposal) => groupAiAPI.approveProposal(groupId, p.id),
    onSuccess: (p) => { setError(null); refresh(); showToast(p.kind === 'task' ? 'Tarefa criada.' : 'Ocorrência criada.') },
    onError: (err, p) => { setError(groupAiErrorMessage(err, 'approve', p.kind)); refresh() },
  })
  const reject = useMutation({
    mutationFn: ({ p, reason }: { p: Proposal; reason: string }) => groupAiAPI.rejectProposal(groupId, p.id, reason.trim() || undefined),
    onSuccess: () => { setError(null); refresh(); showToast('Proposta rejeitada.', 'info') },
    onError: (err) => { setError(groupAiErrorMessage(err, 'reject')); refresh() },
  })
  const edit = useMutation({
    mutationFn: ({ p, body }: { p: Proposal; body: Parameters<typeof groupAiAPI.editProposal>[2] }) => groupAiAPI.editProposal(groupId, p.id, body),
    onSuccess: () => { setError(null); refresh() },
    onError: (err) => { setError(groupAiErrorMessage(err, 'edit_proposal')); refresh() },
  })

  const items = proposals.data?.items ?? []
  const busy = approve.isPending || reject.isPending || edit.isPending

  return (
    <div className="space-y-4 p-4">
      <p className="text-xs leading-relaxed text-text-secondary">
        A IA só propõe: nada é criado até uma pessoa aprovar. Ocorrência é um caso de atendimento; tarefa é um acompanhamento para um atendente. Nenhuma mensagem é enviada ao grupo.
        Para propor, abra a Memória e use &quot;Criar ocorrência&quot; ou &quot;Criar tarefa&quot; num item.
      </p>

      <SelectField label="Filtrar propostas por situação" value={filter} onChange={(e) => setFilter(e.target.value as ProposalStatus | '')}>
        <option value="">Todas as situações</option>
        {(Object.keys(PROPOSAL_STATUS_LABEL) as ProposalStatus[]).map((s) => (
          <option key={s} value={s}>
            {PROPOSAL_STATUS_LABEL[s]}
          </option>
        ))}
      </SelectField>

      {error && <Notice tone="danger" role="alert">{error}</Notice>}
      {proposals.isLoading && <p className="py-4 text-center text-sm text-text-secondary">Carregando propostas...</p>}
      {proposals.isError && (
        <p role="alert" className="text-sm text-status-danger">
          {groupAiErrorMessage(proposals.error, 'load')}
        </p>
      )}
      {proposals.data && items.length === 0 && (
        <p className="rounded-card border border-dashed border-border-subtle px-3 py-6 text-center text-sm text-text-secondary">Nenhuma proposta {filter ? 'com esta situação' : 'ainda'}.</p>
      )}

      <ul className="space-y-3" aria-label="Propostas">
        {items.map((p) => (
          <ProposalCard
            key={`${p.id}-${p.version}`}
            p={p}
            canManage={canManage}
            busy={busy}
            onApprove={() => approve.mutate(p)}
            onReject={(reason) => reject.mutateAsync({ p, reason }).then(() => true, () => false)}
            onSave={(body) => edit.mutateAsync({ p, body }).then(() => true, () => false)}
          />
        ))}
      </ul>
    </div>
  )
}

function ProposalCard({
  p,
  canManage,
  busy,
  onApprove,
  onReject,
  onSave,
}: {
  p: Proposal
  canManage: boolean
  busy: boolean
  onApprove: () => void
  onReject: (reason: string) => Promise<boolean>
  onSave: (body: ReturnType<typeof proposalPatch>) => Promise<boolean>
}) {
  const pending = p.status === 'pending_approval'
  const [title, setTitle] = useState(p.title)
  const [description, setDescription] = useState(p.description)
  const [priority, setPriority] = useState<ProposalPriority>(p.priority)
  const [due, setDue] = useState(toLocalInput(p.due_at))
  const [rejecting, setRejecting] = useState(false)
  const [reason, setReason] = useState('')

  const patch = proposalPatch(p, { title, description, priority, due })
  const dirty = proposalChanged(patch)
  const valid = title.trim().length > 0
  const editable = pending && canManage
  const outcome = proposalOutcome(p)

  return (
    <li className="space-y-2 rounded-card border border-border-subtle bg-surface p-3" aria-label={`${PROPOSAL_KIND_LABEL[p.kind]}: ${p.title}`}>
      <div className="flex flex-wrap items-center gap-1.5">
        <StatusBadge size="sm" status="info">
          {PROPOSAL_KIND_LABEL[p.kind]}
        </StatusBadge>
        <StatusBadge size="sm" status={statusTone[p.status]}>
          {PROPOSAL_STATUS_LABEL[p.status]}
        </StatusBadge>
        <span className="text-xs text-text-tertiary">{p.proposed_by === 'ai' ? 'Rascunho da IA' : 'Rascunho de uma pessoa'} · {formatWhen(p.created_at)}</span>
      </div>

      {editable ? (
        <div className="space-y-2">
          <input aria-label="Título da proposta" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} className={fieldClass} />
          <textarea aria-label="Descrição da proposta" value={description} maxLength={2000} rows={4} onChange={(e) => setDescription(e.target.value)} className={fieldClass} />
          <div className="flex flex-wrap items-center gap-2">
            <SelectField label="Prioridade" value={priority} onChange={(e) => setPriority(e.target.value as ProposalPriority)}>
              {PRIORITIES.map((x) => (
                <option key={x} value={x}>
                  Prioridade {PRIORITY_LABEL[x].toLowerCase()}
                </option>
              ))}
            </SelectField>
            <label className="flex items-center gap-1.5 text-xs text-text-secondary">
              Prazo
              <input type="datetime-local" aria-label="Prazo" value={due} onChange={(e) => setDue(e.target.value)} className={`${fieldClass} !w-auto !py-1.5 text-xs`} />
            </label>
          </div>
        </div>
      ) : (
        <div>
          <p className="break-words text-sm font-medium text-text-primary">{p.title}</p>
          {p.description && <p className="mt-1 break-words whitespace-pre-wrap text-sm text-text-secondary">{p.description}</p>}
          <p className="mt-1 text-xs text-text-tertiary">
            Prioridade {PRIORITY_LABEL[p.priority].toLowerCase()}
            {p.due_at ? ` · prazo ${formatWhen(p.due_at)}` : ''}
          </p>
        </div>
      )}

      {outcome && <Notice tone="success" role="status">{outcome}</Notice>}
      {p.status === 'rejected' && (
        <p className="text-xs text-text-secondary">
          Rejeitada{p.decided_at ? ` em ${formatWhen(p.decided_at)}` : ''}
          {p.reject_reason ? `: ${p.reject_reason}` : '.'}
        </p>
      )}
      {p.status === 'failed' && <Notice tone="danger">A criação falhou. Nada foi criado.</Notice>}

      {editable && !rejecting && (
        <div className="flex flex-wrap items-center gap-2 border-t border-border-subtle pt-2">
          <Button size="sm" variant="primary" disabled={busy || !valid || dirty} onClick={onApprove}>
            {p.kind === 'task' ? 'Aprovar e criar tarefa' : 'Aprovar e criar ocorrência'}
          </Button>
          <Button size="sm" variant="secondary" disabled={busy || !dirty || !valid} onClick={() => void onSave(patch)}>
            Salvar alterações
          </Button>
          <MiniButton danger disabled={busy} onClick={() => setRejecting(true)} className="ml-auto">
            Rejeitar
          </MiniButton>
          {dirty && <p className="w-full text-xs text-text-tertiary">Salve as alterações antes de aprovar.</p>}
        </div>
      )}
      {editable && rejecting && (
        <div className="space-y-2 border-t border-border-subtle pt-2">
          <textarea aria-label="Motivo da rejeição (opcional)" placeholder="Motivo (opcional)" rows={2} maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} className={fieldClass} />
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => { setRejecting(false); setReason('') }}>
              Voltar
            </Button>
            <Button size="sm" variant="danger" disabled={busy} onClick={() => void onReject(reason)}>
              Confirmar rejeição
            </Button>
          </div>
        </div>
      )}
    </li>
  )
}
