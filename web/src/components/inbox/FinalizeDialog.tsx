import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Button, Input, Modal, TextArea } from '../primitives'
import {
  attendanceAPI,
  attendanceErrorMessage,
  CLOSE_REASON_LABEL,
  FOLLOW_UP_KIND_LABEL,
  type CloseReason,
  type FollowUpInput,
  type FollowUpKind,
} from '../../lib/attendance'
import { getTenantId } from '../../lib/session'

interface Props {
  open: boolean
  conversationId: string
  contactName?: string
  onClose: () => void
  onFinalized?: () => void
}

interface DraftItem {
  key: number
  kind: FollowUpKind
  text: string
  due: string // yyyy-mm-dd ou vazio
  ai?: boolean // sugerido pela IA: a pessoa confere e pode editar ou remover
}

const selectClass = 'w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary'

// Finalizar atendimento (ADR-0020): encerra o episódio, não o chat. O que ficou pendente ou foi prometido é listado aqui e
// aparece na próxima conversa do mesmo contato.
export default function FinalizeDialog({ open, conversationId, contactName, onClose, onFinalized }: Props) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [reason, setReason] = useState<CloseReason>('resolved')
  const [summary, setSummary] = useState('')
  const [note, setNote] = useState('')
  const [items, setItems] = useState<DraftItem[]>([])
  const [error, setError] = useState<string | null>(null)
  const [aiDraft, setAiDraft] = useState<string | null>(null) // o texto exato que a IA sugeriu para o resumo
  const [aiNote, setAiNote] = useState<string | null>(null)

  const finalize = useMutation({
    mutationFn: () => {
      const follow_ups: FollowUpInput[] = items
        .filter((i) => i.text.trim() !== '')
        .map((i) => ({ kind: i.kind, text: i.text.trim(), due_at: i.due ? new Date(`${i.due}T12:00:00`).toISOString() : null }))
      // O resumo só vale como "da IA" enquanto estiver exatamente como a IA o escreveu; se a pessoa editou, ela o confirmou.
      const summary_truth = aiDraft !== null && summary.trim() === aiDraft.trim() && summary.trim() !== '' ? 'ai_inferred' : 'agent_confirmed'
      return attendanceAPI.finalize(conversationId, { reason, note: note.trim(), summary: summary.trim(), summary_truth, follow_ups })
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['inbox-conversations', tenantId] })
      for (const key of ['inbox-context', 'inbox-conversation-detail', 'inbox-messages', 'attendance-context']) {
        void qc.invalidateQueries({ queryKey: [key, tenantId, conversationId] })
      }
      setItems([])
      setSummary('')
      setNote('')
      setAiDraft(null)
      setAiNote(null)
      setError(null)
      onFinalized?.()
      onClose()
    },
    onError: (err) => setError(attendanceErrorMessage(err)),
  })

  const suggest = useMutation({
    mutationFn: () => attendanceAPI.suggestClosing(conversationId),
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
    onError: (err) => { setAiNote(null); setError(attendanceErrorMessage(err)) },
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
        <div className="flex items-center justify-between gap-2">
          <Button variant="secondary" size="sm" isLoading={suggest.isPending} disabled={suggest.isPending || finalize.isPending} onClick={() => suggest.mutate()}>Sugerir com IA</Button>
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
        <Input label="Observação interna (opcional)" maxLength={1000} value={note} onChange={(e) => setNote(e.target.value)} />
        <p className="text-xs text-text-secondary">
          Chamados locais desta conversa serão encerrados. Chamados ligados ao ERP não são alterados. Não cole senhas ou chaves nos textos.
        </p>
        {error && <p role="alert" className="rounded-control bg-status-danger-soft p-2 text-sm text-status-danger">{error}</p>}
      </div>
    </Modal>
  )
}
