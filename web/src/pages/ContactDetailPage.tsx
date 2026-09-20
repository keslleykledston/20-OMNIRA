import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  Avatar,
  ErrorState,
  Icon,
  Skeleton,
  StatusBadge,
} from '../components/primitives'
import { contactErrorMessage, contactsAPI, type Contact } from '../lib/contacts'
import { getTenantId } from '../lib/session'
import { formatPhone, initials } from './ContactsPage'

const STATUS_LABELS: Record<Contact['status'], { label: string; tone: 'success' | 'danger' | 'default' }> = {
  active: { label: 'Ativo', tone: 'success' },
  blocked: { label: 'Bloqueado', tone: 'danger' },
  archived: { label: 'Arquivado', tone: 'default' },
}

function formatDateTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString('pt-BR', {
    day: '2-digit',
    month: 'long',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export default function ContactDetailPage() {
  const { contactId = '' } = useParams()
  const navigate = useNavigate()
  const tenantId = getTenantId()

  const contact = useQuery({
    queryKey: ['contact', tenantId, contactId],
    queryFn: () => contactsAPI.get(contactId),
    retry: false,
  })

  return (
    <div className="space-y-6">
      <button
        type="button"
        onClick={() => navigate('/contacts')}
        className="inline-flex items-center gap-2 text-sm font-medium text-text-secondary hover:text-text-primary transition-colors"
      >
        <Icon name="arrow-left" size={16} /> Contatos
      </button>

      {contact.isLoading && (
        <div className="space-y-4">
          <Skeleton width="w-64" height="h-8" />
          <Skeleton width="w-full" height="h-40" />
        </div>
      )}

      {contact.isError && (
        <ErrorState
          title="Contato indisponível"
          message={contactErrorMessage(contact.error, 'Não foi possível carregar este contato.')}
          action={{ label: 'Voltar para contatos', onClick: () => navigate('/contacts') }}
        />
      )}

      {contact.data && <ContactOverview contact={contact.data} />}
    </div>
  )
}

function ContactOverview({ contact }: { contact: Contact }) {
  const status = STATUS_LABELS[contact.status]

  return (
    // Um perfil de poucos campos esticado até a largura da tela deixa rótulo e
    // valor longe demais um do outro.
    <div className="max-w-3xl space-y-6">
      <header className="flex flex-col gap-4 sm:flex-row sm:items-center">
        <Avatar alt={contact.display_name} initials={initials(contact.display_name)} size="lg" />
        <div className="min-w-0">
          <h1 className="text-section-lg font-semibold text-text-primary truncate">
            {contact.display_name}
          </h1>
          <p className="text-text-secondary tabular-nums">{formatPhone(contact.phone_e164)}</p>
        </div>
        <div className="sm:ml-auto">
          <StatusBadge status={status.tone}>{status.label}</StatusBadge>
        </div>
      </header>

      <section className="rounded-card border border-border-subtle bg-surface">
        <h2 className="border-b border-border-subtle px-6 py-4 text-section-sm font-semibold text-text-primary">
          Perfil
        </h2>
        <dl className="divide-y divide-border-subtle">
          <CopyableField label="Telefone" value={formatPhone(contact.phone_e164)} copyValue={contact.phone_e164} />
          {contact.email ? (
            <CopyableField label="E-mail" value={contact.email} copyValue={contact.email} />
          ) : (
            <Field label="E-mail" value="Não informado" muted />
          )}
          {/* A situação já aparece no badge do cabeçalho; repetir aqui só polui. */}
          <Field label="Primeiro contato" value={formatDateTime(contact.created_at)} />
          <Field label="Última atualização" value={formatDateTime(contact.updated_at)} />
        </dl>
      </section>
    </div>
  )
}

function Field({ label, value, muted = false }: { label: string; value: string; muted?: boolean }) {
  return (
    <div className="flex flex-col gap-1 px-6 py-4 sm:flex-row sm:items-center sm:gap-6">
      <dt className="w-40 shrink-0 text-sm text-text-secondary">{label}</dt>
      <dd className={muted ? 'text-text-tertiary' : 'text-text-primary'}>{value}</dd>
    </div>
  )
}

function CopyableField({ label, value, copyValue }: { label: string; value: string; copyValue: string }) {
  const [copied, setCopied] = useState(false)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(copyValue)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard is unavailable outside a secure context; the value stays
      // selectable on screen, so there is nothing to recover from.
    }
  }

  return (
    <div className="flex flex-col gap-1 px-6 py-4 sm:flex-row sm:items-center sm:gap-6">
      <dt className="w-40 shrink-0 text-sm text-text-secondary">{label}</dt>
      <dd className="flex items-center gap-2 text-text-primary">
        <span className="tabular-nums">{value}</span>
        <button
          type="button"
          onClick={copy}
          aria-label={`Copiar ${label.toLowerCase()}`}
          className="rounded-control p-1 text-text-tertiary hover:bg-surface-hover hover:text-text-primary transition-colors"
        >
          <Icon name="copy" size={16} />
        </button>
        {copied && <span className="text-sm text-status-success">Copiado</span>}
      </dd>
    </div>
  )
}
