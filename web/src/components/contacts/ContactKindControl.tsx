import { useState } from 'react'
import clsx from 'clsx'
import { Button, ConfirmDialog } from '../primitives'
import { CONTACT_KIND_LABEL, contactKindErrorMessage, contactsAPI, type ContactKind } from '../../lib/contacts'

interface Props {
  contactId: string
  kind: ContactKind
  contactName?: string
  /** Called after the API accepted the new kind, so the parent can refresh what depends on it. */
  onChanged: (kind: ContactKind) => void
}

// Lets whoever attends say who this contact is. Spam is the one destructive-feeling choice (the
// conversation leaves the queue and the default list), so it asks first; it is never deleted, it has
// its own Inbox ("Spam"), and "Não é spam" there undoes a false positive.
export function ContactKindControl({ contactId, kind, contactName, onChanged }: Props) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmSpam, setConfirmSpam] = useState(false)

  const change = async (next: ContactKind) => {
    if (next === kind || pending) return
    setPending(true)
    setError(null)
    try {
      await contactsAPI.setKind(contactId, next)
      setConfirmSpam(false)
      onChanged(next)
    } catch (err) {
      setError(contactKindErrorMessage(err))
      setConfirmSpam(false)
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="p-4 border-b border-border-subtle" role="group" aria-label="Tipo de contato">
      <h4 className="text-xs font-semibold text-text-tertiary mb-3 uppercase">Tipo de contato</h4>

      {kind === 'spam' ? (
        <div className="rounded-control border border-status-danger-border bg-status-danger-soft p-3 text-xs text-status-danger">
          <p className="font-semibold">Marcado como spam</p>
          <p className="mt-1">
            As conversas deste contato ficam só na caixa Spam e não entram na fila. Se foi engano, restaure.
          </p>
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="secondary" onClick={() => void change('other')} disabled={pending}>
              Não é spam
            </Button>
            <Button size="sm" variant="tertiary" onClick={() => void change('customer')} disabled={pending}>
              É cliente
            </Button>
          </div>
        </div>
      ) : (
        <>
          <div className="flex gap-1.5">
            {(['customer', 'other', 'agent'] as const).map((k) => (
              <button
                key={k}
                type="button"
                aria-pressed={kind === k}
                disabled={pending}
                onClick={() => void change(k)}
                className={clsx(
                  'px-3 py-1 text-xs font-medium rounded-pill transition-colors disabled:opacity-60',
                  kind === k ? 'bg-accent-primary text-white' : 'bg-surface-muted text-text-secondary hover:bg-surface-tertiary',
                )}
              >
                {CONTACT_KIND_LABEL[k]}
              </button>
            ))}
          </div>
          <button
            type="button"
            disabled={pending}
            onClick={() => setConfirmSpam(true)}
            className="mt-3 text-xs font-medium text-status-danger hover:underline disabled:opacity-60"
          >
            Marcar como spam ou golpe
          </button>
        </>
      )}

      {kind === 'agent' && (
        <p className="mt-2 text-xs text-text-tertiary">Agente da equipe K3G: a conversa não entra na fila de atendimento.</p>
      )}

      {error && (
        <p role="alert" className="mt-2 text-xs text-status-danger">
          {error}
        </p>
      )}

      <ConfirmDialog
        open={confirmSpam}
        title="Marcar como spam?"
        message={`${contactName ? contactName + ' deixa' : 'Este contato deixa'} a fila e a lista de conversas e passa para a caixa Spam. Nada é apagado e você pode restaurar depois.`}
        confirmLabel="Marcar como spam"
        destructive
        isPending={pending}
        onConfirm={() => void change('spam')}
        onCancel={() => setConfirmSpam(false)}
      />
    </div>
  )
}
