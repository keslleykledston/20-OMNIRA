import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Input, Modal, TextArea } from '../primitives'
import {
  attendanceAPI,
  attendanceErrorMessage,
  type DelegatedTarget,
  CLOSE_REASON_LABEL,
  FOLLOW_UP_KIND_LABEL,
  type CloseReason,
  type FollowUpInput,
  type FarewellState,
  type FollowUpKind,
} from '../../lib/attendance'
import { getTenantId } from '../../lib/session'
import { isActing } from '../../lib/acting'
import { useAccess } from '../../lib/useAccess'
import { useConversationChannel } from '../../lib/channelLines'
import { BUILTIN_RESULTS, serviceAPI } from '../../lib/serviceContext'
import type { ConversationItem } from '../../types/api'

interface Props {
  open: boolean
  conversationId: string
  contactName?: string
  onClose: () => void
  // farewell: what became of the closing notice when one was asked for (undefined when none was)
  onFinalized?: (farewell?: FarewellState) => void
  // Set when the conversation is opened in the Hub's unified inbox: the instance and the Hub it is served through, and what the server already said about it
  // (the grant may reply; the person holds it). The dialog then acts on THAT instance, not on the session's own tenant.
  hub?: DelegatedTarget & { canReply: boolean; hasOwner: boolean }
}

interface DraftItem {
  key: number
  kind: FollowUpKind
  text: string
  due: string // yyyy-mm-dd ou vazio
  ai?: boolean // sugerido pela IA: a pessoa confere e pode editar ou remover
}

const DEFAULT_FAREWELL = 'Seu atendimento foi encerrado. Se precisar de algo, é só nos escrever por aqui. Obrigado!'

const selectClass = 'w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary'

// Finalizar atendimento (ADR-0020): encerra o episódio, não o chat. O que ficou pendente ou foi prometido é listado aqui e
// aparece na próxima conversa do mesmo contato.
export default function FinalizeDialog({ open, conversationId, contactName, onClose, onFinalized, hub }: Props) {
  const tenantId = hub?.tenantId ?? getTenantId()
  const target: DelegatedTarget | undefined = hub ? { tenantId: hub.tenantId, hubId: hub.hubId } : undefined
  const qc = useQueryClient()
  const [reason, setReason] = useState<CloseReason>('resolved')
  const [serviceResult, setServiceResult] = useState('') // '' = the automatic result for the reason
  // the company's own result codes; the answer also says whether the service module is on (404: it is not, and the choice is not offered)
  const codes = useQuery({ queryKey: ['finalize-service-codes', hub?.hubId ?? 'member', tenantId], queryFn: () => serviceAPI.resultCodes(target), enabled: open, retry: false, staleTime: 60_000 })
  const [summary, setSummary] = useState('')
  const [note, setNote] = useState('')
  const [items, setItems] = useState<DraftItem[]>([])
  const [error, setError] = useState<string | null>(null)
  const [aiDraft, setAiDraft] = useState<string | null>(null) // o texto exato que a IA sugeriu para o resumo
  const [aiNote, setAiNote] = useState<string | null>(null)
  // Closing message to the customer. Offered only when free text can actually go out right now: the attendance must have an
  // owner (sending requires it) and, on the official line, the 24 h window must be open.
  // Attending through a Hub (ADR-0040 phase 05b): the channel lookup is not part of the delegated context, so the closing notice is offered whenever the person may
  // reply (a closed 24 h window comes back as an error and the person can finalize without notifying). The AI draft (phase 07) is offered only with the key ai.assist:
  // the text of the conversation goes to the platform's AI provider.
  const delegated = isActing() || !!hub
  const { can } = useAccess()
  const channel = useConversationChannel(open && !delegated ? conversationId : null)
  const cached = qc.getQueryData<ConversationItem>(['inbox-context', tenantId, conversationId]) ?? qc.getQueryData<ConversationItem>(['inbox-conversation-detail', tenantId, conversationId])
  const hasOwner = hub ? hub.hasOwner : !!cached?.assigned_to_user_id
  const windowClosed = !delegated && !!channel.data && channel.data.window_required && !channel.data.window_open
  const canNotify = hasOwner && !windowClosed && (delegated ? (hub ? hub.canReply : can('conversation.reply')) : channel.data?.can_send_text !== false)
  const [notify, setNotify] = useState(true)
  const [farewell, setFarewell] = useState(DEFAULT_FAREWELL)
  // The closing notice travels WITH the finalize request: the server queues it in the same transaction that closes the conversation, so it never goes out for an
  // attendance that was not finalized, and a refusal (closed 24 h window, no line) does not stop the finalize; the answer says what happened.
  const wantsNotice = () => canNotify && notify && farewell.trim() !== ''

  const finalize = useMutation({
    mutationFn: async () => {
      const follow_ups: FollowUpInput[] = items
        .filter((i) => i.text.trim() !== '')
        .map((i) => ({ kind: i.kind, text: i.text.trim(), due_at: i.due ? new Date(`${i.due}T12:00:00`).toISOString() : null }))
      // O resumo só vale como "da IA" enquanto estiver exatamente como a IA o escreveu; se a pessoa editou, ela o confirmou.
      const summary_truth = aiDraft !== null && summary.trim() === aiDraft.trim() && summary.trim() !== '' ? 'ai_inferred' : 'agent_confirmed'
      const asked = wantsNotice()
      const res = await attendanceAPI.finalize(conversationId, {
        reason, note: note.trim(), summary: summary.trim(), summary_truth, follow_ups, ...(asked ? { farewell: farewell.trim() } : {}),
        ...(serviceResult && reason !== 'no_response' ? { service_result: serviceResult } : {}),
      }, target)
      return { asked, changed: res.changed, farewell: res.farewell }
    },
    onSuccess: (out) => {
      void qc.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] })
      for (const key of ['inbox-context', 'inbox-conversation-detail', 'inbox-messages', 'attendance-context']) {
        void qc.invalidateQueries({ queryKey: [key, tenantId, conversationId] })
      }
      if (hub) {
        void qc.invalidateQueries({ queryKey: ['hub-item', hub.hubId] })
        void qc.invalidateQueries({ queryKey: ['hub-inbox', hub.hubId] })
      }
      setItems([])
      setSummary('')
      setNote('')
      setAiDraft(null)
      setAiNote(null)
      setError(null)
      // an idempotent replay (changed=false: the first call already finalized, maybe with the answer lost) says nothing about the notice: never claim it was not sent
      onFinalized?.(out.asked && out.changed ? out.farewell ?? 'unavailable' : undefined)
      onClose()
    },
    onError: (err) => setError(attendanceErrorMessage(err, delegated)),
  })

  const suggest = useMutation({
    mutationFn: () => attendanceAPI.suggestClosing(conversationId, target),
    onSuccess: (s) => {
      setError(null)
      setSummary(s.summary)
      setAiDraft(s.summary)
      setAiNote(`Rascunho da IA com base em ${s.based_on_messages} mensagem(ns). Confira e edite antes de finalizar.`)
      setItems((cur) => {
        let k = cur.reduce((m, i) => Math.max(m, i.key), 0)
        return [...cur, ...s.follow_ups.map((f) => ({ key: ++k, kind: f.kind, text: f.text, due: '', ai: true }))].slice(0, 20)
      })
    },
    onError: (err) => { setAiNote(null); setError(attendanceErrorMessage(err, delegated)) },
  })

  let seq = items.reduce((m, i) => Math.max(m, i.key), 0)
  const add = () => setItems((cur) => [...cur, { key: ++seq, kind: 'pending', text: '', due: '' }])
  const patch = (key: number, p: Partial<DraftItem>) => setItems((cur) => cur.map((i) => (i.key === key ? { ...i, ...p } : i)))

  return (
    <Modal
      open={open}
      title="Finalizar atendimento"
      description={`Encerra este atendimento${contactName ? ` de ${contactName}` : ''}. Se o contato escrever de novo, abre-se um atendimento novo.`}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancelar</Button>
          <Button isLoading={finalize.isPending} disabled={finalize.isPending} onClick={() => finalize.mutate()}>Finalizar atendimento</Button>
        </>
      }
    >
      <div className="space-y-4">
        <label className="block">
          <span className="mb-2 block text-sm font-medium text-text-primary">Motivo</span>
          <select aria-label="Motivo" className={selectClass} value={reason} onChange={(e) => setReason(e.target.value as CloseReason)}>
            {(Object.keys(CLOSE_REASON_LABEL) as CloseReason[]).map((r) => <option key={r} value={r}>{CLOSE_REASON_LABEL[r]}</option>)}
          </select>
        </label>
        {codes.isSuccess && (
          <label className="block">
            <span className="mb-2 block text-sm font-medium text-text-primary">Resultado do atendimento</span>
            <select aria-label="Resultado do atendimento" className={selectClass} value={reason === 'no_response' ? '' : serviceResult} disabled={reason === 'no_response'} onChange={(e) => setServiceResult(e.target.value)}>
              <option value="">Automático (pelo motivo)</option>
              {BUILTIN_RESULTS.map((r) => <option key={r.code} value={r.code}>{r.label}</option>)}
              {(codes.data ?? []).filter((c) => !BUILTIN_RESULTS.some((b) => b.code === c.code)).map((c) => <option key={c.id} value={c.code}>{c.label}</option>)}
            </select>
            <span className="mt-1 block text-xs text-text-tertiary">
              {reason === 'no_response' ? 'Sem resposta do cliente: o protocolo encerra como abandonado.' : 'O resultado fica no protocolo. Só uma tarefa, ocorrência ou ordem de serviço registra algo no ERP.'}
            </span>
          </label>
        )}
        <div className="flex items-center justify-between gap-2">
          {!hub && (!delegated || can('ai.assist')) ? (
            <Button variant="secondary" size="sm" isLoading={suggest.isPending} disabled={suggest.isPending || finalize.isPending} onClick={() => suggest.mutate()}>Sugerir com IA</Button>
          ) : (
            <span />
          )}
          {aiNote && <p role="status" className="m-0 text-xs text-text-secondary">{aiNote}</p>}
        </div>
        <TextArea
          label="Resumo do atendimento"
          helperText="O que foi tratado. A próxima pessoa que atender este contato vê este resumo."
          rows={3}
          maxLength={4000}
          value={summary}
          onChange={(e) => setSummary(e.target.value)}
        />
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-text-primary">O que ficou pendente ou foi prometido</legend>
          {items.length === 0 && <p className="text-xs text-text-tertiary">Nada a registrar. Adicione se algo ficou em aberto.</p>}
          {items.map((i, n) => (
            <div key={i.key} className="space-y-2 rounded-control border border-border-subtle p-2">
              {i.ai && <p className="m-0 text-xs text-text-tertiary">Sugerido pela IA: confira antes de finalizar.</p>}
              <div className="grid grid-cols-2 gap-2">
                <select aria-label={`Tipo do item ${n + 1}`} className={selectClass} value={i.kind} onChange={(e) => patch(i.key, { kind: e.target.value as FollowUpKind })}>
                  {(Object.keys(FOLLOW_UP_KIND_LABEL) as FollowUpKind[]).map((k) => <option key={k} value={k}>{FOLLOW_UP_KIND_LABEL[k]}</option>)}
                </select>
                <input aria-label={`Prazo do item ${n + 1}`} title="Prazo (opcional)" type="date" className={selectClass} value={i.due} onChange={(e) => patch(i.key, { due: e.target.value })} />
              </div>
              <div className="flex items-center gap-2">
                <input aria-label={`Texto do item ${n + 1}`} className={`${selectClass} min-w-0 flex-1`} maxLength={500} placeholder="Ex.: ligar amanhã com o resultado da visita" value={i.text} onChange={(e) => patch(i.key, { text: e.target.value })} />
                <Button variant="tertiary" size="sm" aria-label={`Remover item ${n + 1}`} onClick={() => setItems((cur) => cur.filter((x) => x.key !== i.key))}>Remover</Button>
              </div>
            </div>
          ))}
          {items.length < 20 && <Button variant="secondary" size="sm" onClick={add}>Adicionar item</Button>}
        </fieldset>
        {canNotify ? (
          <fieldset className="space-y-2 rounded-control border border-border-subtle p-3">
            <label className="flex items-center gap-2 text-sm font-medium text-text-primary">
              <input type="checkbox" checked={notify} onChange={(e) => setNotify(e.target.checked)} />
              Avisar o cliente que o atendimento foi encerrado
            </label>
            {notify && (
              <TextArea aria-label="Mensagem de encerramento" rows={2} maxLength={1000} value={farewell} onChange={(e) => setFarewell(e.target.value)} />
            )}
          </fieldset>
        ) : (
          <p className="text-xs text-text-tertiary">
            {!hasOwner
              ? 'Para avisar o cliente do encerramento, assuma o atendimento antes de finalizar.'
              : windowClosed
                ? 'A janela de 24 h do WhatsApp oficial fechou: não é possível enviar texto livre ao cliente.'
                : 'O canal desta conversa não está disponível para enviar o aviso.'}
          </p>
        )}
        <Input label="Observação interna (opcional)" maxLength={1000} value={note} onChange={(e) => setNote(e.target.value)} />
        <p className="text-xs text-text-secondary">
          Chamados locais desta conversa serão encerrados. Chamados ligados ao ERP não são alterados. Não cole senhas ou chaves nos textos.
        </p>
        {error && <p role="alert" className="rounded-control bg-status-danger-soft p-2 text-sm text-status-danger">{error}</p>}
      </div>
    </Modal>
  )
}
