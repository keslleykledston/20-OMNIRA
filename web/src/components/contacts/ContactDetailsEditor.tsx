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
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    setEditing(false)
    setError('')
  }, [contactId])

  const save = useMutation({
    mutationFn: () => contactEditAPI.update(contactId, { display_name: name.trim(), email: email.trim() }),
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
        <button
          type="button"
          onClick={() => {
            setName(c.display_name)
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
        if (name.trim()) save.mutate()
      }}
    >
      <label className="block text-xs text-text-secondary">
        Nome
        <input aria-label="Nome do contato" className={FIELD + ' mt-1'} maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />
      </label>
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
        <Button size="sm" type="submit" disabled={!name.trim() || save.isPending}>
          Salvar
        </Button>
        <Button size="sm" variant="tertiary" type="button" disabled={save.isPending} onClick={() => setEditing(false)}>
          Cancelar
        </Button>
      </div>
    </form>
  )
}
