import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '../primitives'
import { contactEditAPI, contactEditErrorMessage, contactsAPI } from '../../lib/contacts'
import { getTenantId } from '../../lib/session'

const FIELD =
  'w-full rounded-control border border-border-subtle bg-surface px-2 py-1.5 text-xs text-text-primary ' +
  'focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary'

// Edit who this person is: name and e-mail (the phone is their identity and stays). Anyone who attends can do it.
export function ContactDetailsEditor({ contactId, onChanged }: { contactId: string; onChanged?: () => void }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const key = ['contact', tenantId, contactId]
  const contact = useQuery({ queryKey: key, queryFn: () => contactsAPI.get(contactId), retry: false })
  const [editing, setEditing] = useState(false)
  const [alias, setAlias] = useState('')
  const [email, setEmail] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    setEditing(false)
    setError('')
  }, [contactId])

  const save = useMutation({
    // an empty alias CLEARS it: the name declared on WhatsApp is shown again on its own
    mutationFn: () => contactEditAPI.update(contactId, { alias: alias.trim(), email: email.trim() }),
    onSuccess: (c) => {
      qc.setQueryData(key, c)
      setEditing(false)
      setError('')
      onChanged?.()
    },
    onError: (e) => setError(contactEditErrorMessage(e)),
  })

  if (!contact.data) return null
  const c = contact.data
  if (!editing) {
    return (
      <div className="px-4 pb-3">
        {c.email && <p className="text-sm text-text-secondary break-words">{c.email}</p>}
        {c.alias && c.whatsapp_name && <p className="text-[11px] text-text-tertiary break-words">Nome no WhatsApp: {c.whatsapp_name}</p>}
        <button
          type="button"
          onClick={() => {
            setAlias(c.alias ?? '')
            setEmail(c.email ?? '')
            setEditing(true)
          }}
          className="mt-1 text-xs font-medium text-accent-primary hover:underline"
        >
          Editar contato
        </button>
      </div>
    )
  }
  return (
    <form
      aria-label="Editar contato"
      className="space-y-2 px-4 pb-3"
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate()
      }}
    >
      <label className="block text-xs text-text-secondary">
        Apelido (nome exibido)
        <input
          aria-label="Apelido do contato"
          className={FIELD + ' mt-1'}
          maxLength={200}
          placeholder={c.whatsapp_name || c.display_name}
          value={alias}
          onChange={(e) => setAlias(e.target.value)}
        />
      </label>
      <p className="text-[11px] text-text-tertiary">
        {c.whatsapp_name ? `Nome no WhatsApp: ${c.whatsapp_name}. ` : ''}Apague o apelido para voltar ao nome do WhatsApp.
      </p>
      <label className="block text-xs text-text-secondary">
        E-mail
        <input aria-label="E-mail do contato" type="email" className={FIELD + ' mt-1'} maxLength={254} value={email} onChange={(e) => setEmail(e.target.value)} />
      </label>
      {error && (
        <p role="alert" className="text-xs text-status-danger">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <Button size="sm" type="submit" disabled={save.isPending}>
          Salvar
        </Button>
        <Button size="sm" variant="tertiary" type="button" disabled={save.isPending} onClick={() => setEditing(false)}>
          Cancelar
        </Button>
      </div>
    </form>
  )
}
