import { useState } from 'react'
import { Icon, StatusBadge } from '../../primitives'
import type { MemoryItem, ProposalKind } from '../../../lib/groupAi'
import { KIND_LABEL, SOURCE_LABEL, STATUS_LABEL, TRUTH_LABEL, evidenceLine, formatWhen } from '../../../lib/groupAiModel'
import { MiniButton, fieldClass } from './ui'

interface Props {
  item: MemoryItem
  canManage: boolean
  proposalsAvailable: boolean
  busy: boolean
  onConfirm: (item: MemoryItem) => void
  onPin: (item: MemoryItem) => void
  onToggleResolved: (item: MemoryItem) => void
  onDelete: (item: MemoryItem) => void
  onDraft: (item: MemoryItem, kind: ProposalKind) => void
  /** Resolves true when saved (the editor closes), false when it failed (it stays open with the person's text). */
  onSaveText: (item: MemoryItem, summary: string) => Promise<boolean>
}

const truthTone = { human_confirmed: 'success', ai_inferred: 'warning', said_in_group: 'default' } as const

// One memory item. The summary, the author label and the evidence quotes are shown exactly as the server sent them
// (as text, never as markup): nothing here is composed or guessed on the screen.
export function MemoryItemCard({ item, canManage, proposalsAvailable, busy, onConfirm, onPin, onToggleResolved, onDelete, onDraft, onSaveText }: Props) {
  const [evidenceOpen, setEvidenceOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState(item.summary)
  const superseded = item.status === 'superseded'

  const save = async () => {
    if (!text.trim() || text.trim() === item.summary) {
      setEditing(false)
      return
    }
    if (await onSaveText(item, text.trim())) setEditing(false)
  }

  return (
    <li className="rounded-card border border-border-subtle bg-surface p-3" aria-label={`${KIND_LABEL[item.kind]}: ${item.summary}`}>
      <div className="flex flex-wrap items-center gap-1.5">
        <StatusBadge size="sm" status="info">
          {KIND_LABEL[item.kind]}
        </StatusBadge>
        <StatusBadge size="sm" status={item.status === 'open' ? 'warning' : 'default'}>
          {STATUS_LABEL[item.status]}
        </StatusBadge>
        <StatusBadge size="sm" status={truthTone[item.truth_level]}>
          {TRUTH_LABEL[item.truth_level]}
        </StatusBadge>
        {item.tier === 'consolidated' && (
          <StatusBadge size="sm" status="default">
            Condensado
          </StatusBadge>
        )}
        {item.pinned && (
          <span className="inline-flex items-center gap-1 text-xs font-medium text-accent-primary">
            <Icon name="flag" size={14} />
            Fixado
          </span>
        )}
      </div>

      {editing ? (
        <div className="mt-2 space-y-2">
          <textarea
            aria-label="Texto do item"
            value={text}
            maxLength={600}
            rows={3}
            onChange={(e) => setText(e.target.value)}
            className={fieldClass}
          />
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs text-text-tertiary">{text.length}/600 · ao salvar, o item passa a ser confirmado por pessoa</span>
            <div className="flex gap-1">
              <MiniButton onClick={() => { setEditing(false); setText(item.summary) }}>Cancelar</MiniButton>
              <MiniButton disabled={busy || !text.trim()} onClick={() => void save()}>
                Salvar
              </MiniButton>
            </div>
          </div>
        </div>
      ) : (
        <p className="mt-2 break-words whitespace-pre-wrap text-sm text-text-primary">{item.summary}</p>
      )}

      <p className="mt-1.5 text-xs text-text-secondary">
        {item.actor_label ? <>Quem: {item.actor_label} · </> : null}
        Origem: {SOURCE_LABEL[item.source]} · {formatWhen(item.last_reinforced_at)}
      </p>

      <div className="mt-1">
        {item.evidence.length > 0 ? (
          <button
            type="button"
            aria-expanded={evidenceOpen}
            onClick={() => setEvidenceOpen((v) => !v)}
            className="inline-flex items-center gap-1 rounded-control py-0.5 text-xs text-text-secondary hover:text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary"
          >
            <Icon name="chevron-down" size={14} className={evidenceOpen ? 'rotate-180' : undefined} />
            {evidenceOpen ? 'Ocultar evidência' : `Ver evidência (${item.evidence.length})`}
          </button>
        ) : (
          <p className="text-xs text-text-tertiary">{evidenceLine(item)}</p>
        )}
        {evidenceOpen && (
          <ul className="mt-1 space-y-1.5" aria-label="Evidência">
            {item.evidence.map((e, i) => (
              <li key={i} className="rounded-control border-l-2 border-border-light bg-surface-muted px-2 py-1.5">
                {e.quote ? (
                  <q className="break-words text-xs text-text-primary">{e.quote}</q>
                ) : (
                  <span className="text-xs italic text-text-tertiary">Trecho removido (histórico apagado ou item condensado).</span>
                )}
                <span className="mt-0.5 block text-[11px] text-text-tertiary">{formatWhen(e.at)}</span>
              </li>
            ))}
          </ul>
        )}
      </div>

      {canManage && !editing && (
        <div className="mt-2 flex flex-wrap items-center gap-x-1 gap-y-0.5 border-t border-border-subtle pt-2">
          {!superseded && item.truth_level !== 'human_confirmed' && (
            <MiniButton disabled={busy} onClick={() => onConfirm(item)}>
              Confirmar
            </MiniButton>
          )}
          {!superseded && (
            <MiniButton disabled={busy} onClick={() => onPin(item)}>
              {item.pinned ? 'Desafixar' : 'Fixar'}
            </MiniButton>
          )}
          {!superseded && (
            <MiniButton disabled={busy} onClick={() => { setText(item.summary); setEditing(true) }}>
              Editar
            </MiniButton>
          )}
          {!superseded && (
            <MiniButton disabled={busy} onClick={() => onToggleResolved(item)}>
              {item.status === 'resolved' ? 'Reabrir' : 'Marcar como resolvido'}
            </MiniButton>
          )}
          {proposalsAvailable && !superseded && (
            <>
              <MiniButton disabled={busy} onClick={() => onDraft(item, 'case')}>
                Criar ocorrência
              </MiniButton>
              <MiniButton disabled={busy} onClick={() => onDraft(item, 'task')}>
                Criar tarefa
              </MiniButton>
            </>
          )}
          <MiniButton danger disabled={busy} onClick={() => onDelete(item)} className="ml-auto">
            Apagar
          </MiniButton>
        </div>
      )}
    </li>
  )
}
