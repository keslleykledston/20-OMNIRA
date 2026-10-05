import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '../primitives'
import { contactEditAPI, contactEditErrorMessage } from '../../lib/contacts'
import { getTenantId } from '../../lib/session'

const FIELD =
  'w-full rounded-control border border-border-subtle bg-surface px-2 py-1.5 text-xs text-text-primary ' +
  'focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary'

function when(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', year: '2-digit', hour: '2-digit', minute: '2-digit' })
}

// Notes that document the context of this contact for whoever attends next. Internal only: never sent to the contact. Each
// has its author and time; only the author edits or removes it.
export function ContactNotes({ contactId }: { contactId: string }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const key = ['contact-notes', tenantId, contactId]
  const notes = useQuery({ queryKey: key, queryFn: () => contactEditAPI.notes(contactId), retry: false })
  const [draft, setDraft] = useState('')
  const [editing, setEditing] = useState<{ id: string; body: string } | null>(null)
  const [error, setError] = useState('')
  const refresh = () => {
    setError('')
    void qc.invalidateQueries({ queryKey: key })
  }
  const fail = (e: unknown) => setError(contactEditErrorMessage(e))
  const add = useMutation({ mutationFn: () => contactEditAPI.addNote(contactId, draft.trim()), onSuccess: () => { setDraft(''); refresh() }, onError: fail })
  const edit = useMutation({ mutationFn: (v: { id: string; body: string }) => contactEditAPI.editNote(contactId, v.id, v.body.trim()), onSuccess: () => { setEditing(null); refresh() }, onError: fail })
  const remove = useMutation({ mutationFn: (id: string) => contactEditAPI.deleteNote(contactId, id), onSuccess: refresh, onError: fail })

  // no permission to read notes: the section does not exist
  if (notes.isError) return null
  const items = notes.data ?? []
  const busy = add.isPending || edit.isPending || remove.isPending
  return (
    <section aria-label="Anotações do contato" className="p-4 border-b border-border-subtle">
      <h4 className="text-xs font-semibold text-text-tertiary mb-3 uppercase">Anotações</h4>
      {items.length === 0 && !notes.isLoading && <p className="mb-2 text-xs text-text-secondary">Nenhuma anotação. Registre aqui o contexto desta pessoa para quem atender depois.</p>}
      <ul className="space-y-2">
        {items.map((n) => (
          <li key={n.id} className="rounded-control bg-surface-muted p-2 text-xs">
            {editing?.id === n.id ? (
              <div className="space-y-2">
                <textarea aria-label="Editar anotação" className={FIELD} rows={3} maxLength={4000} value={editing.body} onChange={(e) => setEditing({ id: n.id, body: e.target.value })} />
                <div className="flex gap-2">
                  <Button size="sm" disabled={!editing.body.trim() || busy} onClick={() => edit.mutate(editing)}>
                    Salvar anotação
                  </Button>
                  <Button size="sm" variant="tertiary" onClick={() => setEditing(null)}>
                    Cancelar
                  </Button>
                </div>
              </div>
            ) : (
              <>
                <p className="whitespace-pre-wrap break-words text-text-primary">{n.body}</p>
                <p className="mt-1 flex flex-wrap items-center gap-2 text-[11px] text-text-tertiary">
                  <span>
                    {n.author_name || 'Alguém da equipe'} · {when(n.created_at)}
                    {n.updated_at !== n.created_at ? ' (editada)' : ''}
                  </span>
                  {n.mine && (
                    <>
                      <button type="button" disabled={busy} onClick={() => setEditing({ id: n.id, body: n.body })} className="text-accent-primary hover:underline">
                        Editar
                      </button>
                      <button type="button" disabled={busy} aria-label="Remover anotação" onClick={() => remove.mutate(n.id)} className="text-status-danger hover:underline">
                        Remover
                      </button>
                    </>
                  )}
                </p>
              </>
            )}
          </li>
        ))}
      </ul>
      <form
        className="mt-3 space-y-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (draft.trim()) add.mutate()
        }}
      >
        <textarea aria-label="Nova anotação" className={FIELD} rows={3} maxLength={4000} placeholder="Ex.: prefere WhatsApp de manhã; contrato renova em outubro…" value={draft} onChange={(e) => setDraft(e.target.value)} />
        <Button size="sm" type="submit" disabled={!draft.trim() || busy}>
          Adicionar anotação
        </Button>
      </form>
      {error && (
        <p role="alert" className="mt-2 text-xs text-status-danger">
          {error}
        </p>
      )}
    </section>
  )
}
