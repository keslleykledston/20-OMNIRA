import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  Avatar,
  EmptyState,
  ErrorState,
  Icon,
  PageHeader,
  Skeleton,
  StatusBadge,
} from '../components/primitives'
import { contactErrorMessage, contactsAPI, type Contact } from '../lib/contacts'
import { getTenantId } from '../lib/session'

const PAGE_SIZE = 20

const STATUS_LABELS: Record<Contact['status'], { label: string; tone: 'success' | 'danger' | 'default' }> = {
  active: { label: 'Ativo', tone: 'success' },
  blocked: { label: 'Bloqueado', tone: 'danger' },
  archived: { label: 'Arquivado', tone: 'default' },
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

// +5511998887766 -> +55 11 99888-7766. Falls back to the raw value for
// anything that is not a Brazilian mobile, since the API stores E.164 from
// several countries.
export function formatPhone(e164: string): string {
  const br = /^\+55(\d{2})(\d{4,5})(\d{4})$/.exec(e164)
  if (!br) return e164
  return `+55 ${br[1]} ${br[2]}-${br[3]}`
}

export function formatDate(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString('pt-BR', { day: '2-digit', month: 'short', year: 'numeric' })
}

export default function ContactsPage() {
  const navigate = useNavigate()
  const tenantId = getTenantId()

  // The API paginates by cursor, so going back is not "page - 1": we keep the
  // cursor that opened each page and pop it to return.
  const [cursorStack, setCursorStack] = useState<string[]>([])
  const cursor = cursorStack[cursorStack.length - 1]

  const contacts = useQuery({
    queryKey: ['contacts', tenantId, cursor ?? 'first'],
    queryFn: () => contactsAPI.list(cursor, PAGE_SIZE),
    retry: false,
  })

  const page = contacts.data
  const items = page?.items ?? []

  return (
    <div className="space-y-6">
      <PageHeader
        title="Contatos"
        description="Pessoas que já conversaram com a sua operação."
      />

      {contacts.isError && (
        <ErrorState
          message={contactErrorMessage(contacts.error)}
          action={{ label: 'Tentar novamente', onClick: () => void contacts.refetch() }}
        />
      )}

      {contacts.isLoading && <ContactsSkeleton />}

      {!contacts.isLoading && !contacts.isError && items.length === 0 && (
        <EmptyState
          icon={<Icon name="contacts" />}
          title={cursorStack.length > 0 ? 'Nada nesta página' : 'Nenhum contato ainda'}
          description={
            cursorStack.length > 0
              ? 'Volte para a página anterior.'
              : 'Assim que uma pessoa enviar a primeira mensagem, ela aparece aqui.'
          }
          action={
            cursorStack.length > 0
              ? { label: 'Voltar', onClick: () => setCursorStack((s) => s.slice(0, -1)) }
              : undefined
          }
        />
      )}

      {!contacts.isLoading && !contacts.isError && items.length > 0 && (
        <>
          {/* Desktop: table. Mobile falls back to cards below. */}
          <div className="hidden md:block overflow-hidden rounded-card border border-border-subtle bg-surface">
            <table className="w-full text-left">
              <thead className="border-b border-border-subtle bg-surface-muted">
                <tr>
                  <th scope="col" className="px-6 py-3 text-sm font-medium text-text-secondary">Nome</th>
                  <th scope="col" className="px-6 py-3 text-sm font-medium text-text-secondary">Telefone</th>
                  <th scope="col" className="px-6 py-3 text-sm font-medium text-text-secondary">E-mail</th>
                  <th scope="col" className="px-6 py-3 text-sm font-medium text-text-secondary">Situação</th>
                  <th scope="col" className="px-6 py-3 text-sm font-medium text-text-secondary">Atualizado</th>
                </tr>
              </thead>
              <tbody>
                {items.map((c) => (
                  <tr
                    key={c.id}
                    onClick={() => navigate(`/contacts/${c.id}`)}
                    className="border-b border-border-subtle last:border-0 cursor-pointer hover:bg-surface-hover transition-colors"
                  >
                    <td className="px-6 py-4">
                      <div className="flex items-center gap-3">
                        <Avatar alt={c.display_name} initials={initials(c.display_name)} size="sm" />
                        <span className="font-medium text-text-primary">{c.display_name}</span>
                      </div>
                    </td>
                    <td className="px-6 py-4 text-text-secondary tabular-nums">{formatPhone(c.phone_e164)}</td>
                    <td className="px-6 py-4 text-text-secondary">{c.email || '—'}</td>
                    <td className="px-6 py-4">
                      <StatusBadge status={STATUS_LABELS[c.status].tone} size="sm">
                        {STATUS_LABELS[c.status].label}
                      </StatusBadge>
                    </td>
                    <td className="px-6 py-4 text-text-secondary">{formatDate(c.updated_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="md:hidden space-y-3">
            {items.map((c) => (
              <button
                key={c.id}
                onClick={() => navigate(`/contacts/${c.id}`)}
                className="w-full text-left rounded-card border border-border-subtle bg-surface p-4 hover:bg-surface-hover transition-colors"
              >
                <div className="flex items-center gap-3">
                  <Avatar alt={c.display_name} initials={initials(c.display_name)} size="md" />
                  <div className="min-w-0 flex-1">
                    <p className="font-medium text-text-primary truncate">{c.display_name}</p>
                    <p className="text-sm text-text-secondary tabular-nums">{formatPhone(c.phone_e164)}</p>
                  </div>
                  <StatusBadge status={STATUS_LABELS[c.status].tone} size="sm">
                    {STATUS_LABELS[c.status].label}
                  </StatusBadge>
                </div>
              </button>
            ))}
          </div>

          <nav className="flex items-center justify-between" aria-label="Paginação">
            <p className="text-sm text-text-secondary">
              {page?.count ?? 0} {page?.count === 1 ? 'contato' : 'contatos'} nesta página
            </p>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => setCursorStack((s) => s.slice(0, -1))}
                disabled={cursorStack.length === 0}
                className="h-10 px-4 rounded-control border border-border-light text-sm font-medium text-text-primary hover:bg-surface-hover disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
              >
                Anterior
              </button>
              <button
                type="button"
                onClick={() => page?.next_cursor && setCursorStack((s) => [...s, page.next_cursor!])}
                disabled={!page?.has_more || !page?.next_cursor}
                className="h-10 px-4 rounded-control border border-border-light text-sm font-medium text-text-primary hover:bg-surface-hover disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
              >
                Próxima
              </button>
            </div>
          </nav>
        </>
      )}
    </div>
  )
}

function ContactsSkeleton() {
  return (
    <div className="rounded-card border border-border-subtle bg-surface p-6 space-y-4">
      {Array.from({ length: 6 }).map((_, i) => (
        <div key={i} className="flex items-center gap-3">
          <Skeleton width="w-8" height="h-8" className="rounded-full shrink-0" />
          <Skeleton width="w-full" height="h-4" />
        </div>
      ))}
    </div>
  )
}
