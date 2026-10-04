import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  Avatar,
  EmptyState,
  ErrorState,
  Icon,
  PageHeader,
  Pagination,
  SearchField,
  Skeleton,
  StatusBadge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from '../components/primitives'
import { ChannelChips } from '../components/contacts/ChannelChips'
import { formatInteraction } from '../lib/contactFormat'
import { CONTACT_KIND_LABEL, contactErrorMessage, contactsAPI, type Contact, type ContactFilters, type ContactKind } from '../lib/contacts'
import { useDebounced } from '../hooks/useDebounced'
import { getTenantId } from '../lib/session'

const PAGE_SIZE = 20

const STATUS_LABELS: Record<Contact['status'], { label: string; tone: 'success' | 'danger' | 'default' }> = {
  active: { label: 'Ativo', tone: 'success' },
  blocked: { label: 'Bloqueado', tone: 'danger' },
  archived: { label: 'Arquivado', tone: 'default' },
}

const KIND_TONE: Record<ContactKind, 'success' | 'danger' | 'default'> = {
  customer: 'success',
  other: 'default',
  spam: 'danger',
}

const SELECT =
  'rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary ' +
  'focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary'

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

  // Search and filters run on the server (every contact, not just the loaded page). Any change starts
  // again from the first page, because a cursor only makes sense for the filters that produced it.
  const [search, setSearch] = useState('')
  const [kind, setKind] = useState<ContactKind | ''>('')
  const [status, setStatus] = useState<Contact['status'] | ''>('')
  const q = useDebounced(search.trim(), 300)
  const filters: ContactFilters = { q: q || undefined, kind: kind || undefined, status: status || undefined }
  const filtering = Boolean(q || kind || status)
  const resetPaging = () => setCursorStack([])

  const contacts = useQuery({
    queryKey: ['contacts', tenantId, cursor ?? 'first', q, kind, status],
    queryFn: () => contactsAPI.list(cursor, PAGE_SIZE, filters),
    retry: false,
  })

  const page = contacts.data
  const items = page?.items ?? []

  return (
    <div className="space-y-6 px-6 py-6 lg:px-8 lg:py-8">
      <PageHeader
        title="Contatos"
        description="Pessoas que já conversaram com a sua operação."
      />

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="sm:max-w-sm sm:flex-1">
          <SearchField
            aria-label="Buscar contato"
            placeholder="Buscar por nome, e-mail ou telefone..."
            value={search}
            onChange={(e) => {
              setSearch(e.target.value)
              resetPaging()
            }}
            onClear={() => {
              setSearch('')
              resetPaging()
            }}
          />
        </div>
        <label className="flex items-center gap-2 text-sm text-text-secondary">
          Tipo
          <select
            aria-label="Tipo"
            className={SELECT}
            value={kind}
            onChange={(e) => {
              setKind(e.target.value as ContactKind | '')
              resetPaging()
            }}
          >
            <option value="">Todos</option>
            <option value="customer">{CONTACT_KIND_LABEL.customer}s</option>
            <option value="other">{CONTACT_KIND_LABEL.other}</option>
            <option value="spam">{CONTACT_KIND_LABEL.spam}</option>
          </select>
        </label>
        <label className="flex items-center gap-2 text-sm text-text-secondary">
          Status
          <select
            aria-label="Status"
            className={SELECT}
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as Contact['status'] | '')
              resetPaging()
            }}
          >
            <option value="">Todos</option>
            <option value="active">Ativos</option>
            <option value="blocked">Bloqueados</option>
            <option value="archived">Arquivados</option>
          </select>
        </label>
      </div>

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
          title={cursorStack.length > 0 ? 'Nada nesta página' : filtering ? 'Nenhum contato encontrado' : 'Nenhum contato ainda'}
          description={
            cursorStack.length > 0
              ? 'Volte para a página anterior.'
              : filtering
                ? 'Nenhum contato combina com a busca e os filtros escolhidos.'
                : 'Assim que uma pessoa enviar a primeira mensagem, ela aparece aqui.'
          }
          action={
            cursorStack.length > 0
              ? { label: 'Voltar', onClick: () => setCursorStack((s) => s.slice(0, -1)) }
              : filtering
                ? {
                    label: 'Limpar filtros',
                    onClick: () => {
                      setSearch('')
                      setKind('')
                      setStatus('')
                      resetPaging()
                    },
                  }
                : undefined
          }
        />
      )}

      {!contacts.isLoading && !contacts.isError && items.length > 0 && (
        <>
          {/* Desktop: table. Mobile falls back to cards below. */}
          <div className="hidden md:block">
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeaderCell>Contato</TableHeaderCell>
                  <TableHeaderCell>Telefone</TableHeaderCell>
                  <TableHeaderCell>Canais</TableHeaderCell>
                  <TableHeaderCell>Última interação</TableHeaderCell>
                  <TableHeaderCell>Conversas abertas</TableHeaderCell>
                  <TableHeaderCell>Tipo</TableHeaderCell>
                  <TableHeaderCell>Status</TableHeaderCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {items.map((c) => (
                  <TableRow key={c.id} interactive onClick={() => navigate(`/contacts/${c.id}`)}>
                    <TableCell>
                      <div className="flex items-center gap-3">
                        <Avatar alt={c.display_name} initials={initials(c.display_name)} size="sm" />
                        <div className="min-w-0">
                          <p className="truncate font-medium text-text-primary">{c.display_name}</p>
                          {c.email && <p className="truncate text-xs text-text-secondary">{c.email}</p>}
                        </div>
                      </div>
                    </TableCell>
                    <TableCell className="text-text-secondary tabular-nums whitespace-nowrap">{formatPhone(c.phone_e164)}</TableCell>
                    <TableCell>
                      <ChannelChips channels={c.channels} />
                    </TableCell>
                    <TableCell className="text-text-secondary whitespace-nowrap">{formatInteraction(c.last_interaction_at)}</TableCell>
                    <TableCell className="font-medium tabular-nums text-text-primary">{c.open_conversation_count}</TableCell>
                    <TableCell>
                      <StatusBadge status={KIND_TONE[c.kind]} size="sm">
                        {CONTACT_KIND_LABEL[c.kind]}
                      </StatusBadge>
                    </TableCell>
                    <TableCell>
                      <StatusBadge status={STATUS_LABELS[c.status].tone} size="sm">
                        {STATUS_LABELS[c.status].label}
                      </StatusBadge>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
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
                  <div className="flex flex-col items-end gap-1">
                    <StatusBadge status={KIND_TONE[c.kind]} size="sm">
                      {CONTACT_KIND_LABEL[c.kind]}
                    </StatusBadge>
                    <StatusBadge status={STATUS_LABELS[c.status].tone} size="sm">
                      {STATUS_LABELS[c.status].label}
                    </StatusBadge>
                  </div>
                </div>
                <div className="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs text-text-secondary">
                  <ChannelChips channels={c.channels} />
                  <span>
                    {formatInteraction(c.last_interaction_at)} · {c.open_conversation_count}{' '}
                    {c.open_conversation_count === 1 ? 'conversa aberta' : 'conversas abertas'}
                  </span>
                </div>
              </button>
            ))}
          </div>

          <Pagination
            hasPrevious={cursorStack.length > 0}
            hasNext={Boolean(page?.has_more && page?.next_cursor)}
            onPrevious={() => setCursorStack((s) => s.slice(0, -1))}
            onNext={() => page?.next_cursor && setCursorStack((s) => [...s, page.next_cursor!])}
            label={`${page?.count ?? 0} ${page?.count === 1 ? 'contato' : 'contatos'} nesta página`}
          />
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
