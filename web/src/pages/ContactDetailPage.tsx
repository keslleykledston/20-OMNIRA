import { useState } from 'react'
import ContactTopics from '../components/topics/ContactTopics'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  Avatar,
  Badge,
  ErrorState,
  Icon,
  Skeleton,
  StatusBadge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from '../components/primitives'
import type { IconName } from '../components/primitives'
import { ChannelChip, ChannelChips } from '../components/contacts/ChannelChips'
import { conversationPreview, formatInteraction, messageCountLabel } from '../lib/contactFormat'
import {
  contactErrorMessage,
  contactsAPI,
  type Contact,
  type ContactConversation,
  type ContactTicket,
} from '../lib/contacts'
import { getTenantId } from '../lib/session'
import { formatDate, formatPhone, initials } from './ContactsPage'

// Contact 360 shows only what the backend really knows (CONTACT.360-A): the
// contact, its conversations and its tickets. No tags, owner, document or
// notes exist as data, so none are drawn.

const STATUS_LABELS: Record<Contact['status'], { label: string; tone: 'success' | 'danger' | 'default' }> = {
  active: { label: 'Ativo', tone: 'success' },
  blocked: { label: 'Bloqueado', tone: 'danger' },
  archived: { label: 'Arquivado', tone: 'default' },
}

// Same wording and tones as the Tickets page, so one ticket reads the same
// everywhere in the product.
const TICKET_STATUS: Record<ContactTicket['status'], { label: string; tone: 'success' | 'warning' | 'danger' | 'default' }> = {
  open: { label: 'Aberto', tone: 'danger' },
  in_progress: { label: 'Em progresso', tone: 'warning' },
  waiting: { label: 'Aguardando', tone: 'default' },
  resolved: { label: 'Resolvido', tone: 'success' },
  closed: { label: 'Fechado', tone: 'default' },
}

const TICKET_PRIORITY: Record<ContactTicket['priority'], string> = {
  critical: 'Crítica',
  high: 'Alta',
  medium: 'Média',
  low: 'Baixa',
}

const ACTIVE_TICKET = new Set<ContactTicket['status']>(['open', 'in_progress', 'waiting'])
const SUBRESOURCE_LIMIT = 20

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

// The frozen Inbox deep-link contract: /inbox?conversation_id=<id>.
const inboxLink = (conversationId: string) => `/inbox?conversation_id=${encodeURIComponent(conversationId)}`

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
    <div className="space-y-6 px-6 py-6 lg:px-8 lg:py-8">
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
  const navigate = useNavigate()
  const tenantId = getTenantId()
  const status = STATUS_LABELS[contact.status]
  const firstName = contact.display_name.trim().split(/\s+/)[0] || contact.display_name

  const conversations = useQuery({
    queryKey: ['contact-conversations', tenantId, contact.id],
    queryFn: () => contactsAPI.conversations(contact.id, undefined, SUBRESOURCE_LIMIT),
    retry: false,
  })
  const tickets = useQuery({
    queryKey: ['contact-tickets', tenantId, contact.id],
    queryFn: () => contactsAPI.tickets(contact.id, undefined, SUBRESOURCE_LIMIT),
    retry: false,
  })

  // ticket.read comes from the role matrix: a role without it gets 403, which is
  // a normal state of this screen and not a failure.
  const ticketsForbidden = tickets.isError && (tickets.error as any)?.response?.status === 403
  const ticketItems = tickets.data?.items ?? []
  const activeTickets = tickets.data ? ticketItems.filter((t) => ACTIVE_TICKET.has(t.status)).length : undefined

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-4 sm:flex-row sm:items-center">
        <Avatar alt={contact.display_name} initials={initials(contact.display_name)} size="lg" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-section-lg font-semibold text-text-primary truncate">{contact.display_name}</h1>
            <StatusBadge status={status.tone}>{status.label}</StatusBadge>
          </div>
          <p className="text-text-secondary tabular-nums">{formatPhone(contact.phone_e164)}</p>
          <p className="text-sm text-text-tertiary">Cliente desde {formatDate(contact.created_at)}</p>
        </div>
      </header>

      <div className="grid gap-6 xl:grid-cols-[minmax(0,7fr)_minmax(0,3fr)]">
        <div className="min-w-0 space-y-6">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <StatCard label="Última interação" icon="clock" tone="neutral">
              <span className="text-body-md font-semibold text-text-primary">
                {formatInteraction(contact.last_interaction_at)}
              </span>
            </StatCard>
            <StatCard label="Conversas abertas" icon="conversations" tone="info">
              <Count value={contact.open_conversation_count} />
            </StatCard>
            <StatCard
              label="Tickets ativos"
              icon="tickets"
              tone="warning"
              helperText={tickets.data?.has_more ? `Entre os ${SUBRESOURCE_LIMIT} mais recentes` : undefined}
            >
              <Count value={activeTickets} />
            </StatCard>
            <StatCard label="Canais" icon="channels" tone="neutral">
              <ChannelChips channels={contact.channels} />
            </StatCard>
          </div>

          <ContactTopics contactId={contact.id} />

          <Section title="Histórico de conversas">
            {conversations.isLoading && <ListSkeleton label="Carregando conversas" />}
            {conversations.isError && (
              <InlineError
                message="Não foi possível carregar as conversas."
                onRetry={() => void conversations.refetch()}
              />
            )}
            {conversations.data && conversations.data.items.length === 0 && (
              <EmptyText>Nenhuma conversa ainda.</EmptyText>
            )}
            {conversations.data && conversations.data.items.length > 0 && (
              <ul className="divide-y divide-border-subtle">
                {conversations.data.items.map((c) => (
                  <ConversationRow
                    key={c.id}
                    conversation={c}
                    firstName={firstName}
                    onOpen={() => navigate(inboxLink(c.id))}
                  />
                ))}
              </ul>
            )}
          </Section>

          <Section title="Tickets recentes">
            {tickets.isLoading && <ListSkeleton label="Carregando tickets" />}
            {ticketsForbidden && <EmptyText>Você não tem permissão para ver tickets.</EmptyText>}
            {tickets.isError && !ticketsForbidden && (
              <InlineError message="Não foi possível carregar os tickets." onRetry={() => void tickets.refetch()} />
            )}
            {tickets.data && ticketItems.length === 0 && <EmptyText>Nenhum ticket para este contato.</EmptyText>}
            {tickets.data && ticketItems.length > 0 && (
              <TicketList tickets={ticketItems} onOpen={(conversationId) => navigate(inboxLink(conversationId))} />
            )}
          </Section>
        </div>

        <ProfileCard contact={contact} />
      </div>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-card border border-border-subtle bg-surface" aria-label={title}>
      <h2 className="border-b border-border-subtle px-6 py-4 text-section-sm font-semibold text-text-primary">
        {title}
      </h2>
      {children}
    </section>
  )
}

const TONE_ICON_CLASS = {
  neutral: 'bg-accent-primary-soft text-accent-primary',
  info: 'bg-status-info-soft text-status-info',
  warning: 'bg-status-warning-soft text-status-warning',
} as const

// Like the shared MetricCard, but the label wraps instead of truncating and the
// value can be a chip list or a date, not only a number.
function StatCard({
  label,
  icon,
  tone,
  helperText,
  children,
}: {
  label: string
  icon: IconName
  tone: keyof typeof TONE_ICON_CLASS
  helperText?: string
  children: React.ReactNode
}) {
  return (
    <div role="group" aria-label={label} className="min-w-0 rounded-card border border-border-subtle bg-surface p-5 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-body-sm text-text-secondary">{label}</p>
          <div className="mt-2">{children}</div>
          {helperText && <p className="mt-1.5 text-xs text-text-tertiary">{helperText}</p>}
        </div>
        <span className={`flex-shrink-0 rounded-card p-2.5 ${TONE_ICON_CLASS[tone]}`}>
          <Icon name={icon} size={20} />
        </span>
      </div>
    </div>
  )
}

// A count that is not known yet (loading, no permission, failed) is a dash, never a zero.
function Count({ value }: { value: number | undefined }) {
  return <p className="text-display-md font-bold tabular-nums text-text-primary">{value ?? '—'}</p>
}

function EmptyText({ children }: { children: React.ReactNode }) {
  return <p className="px-6 py-10 text-center text-sm text-text-secondary">{children}</p>
}

function ListSkeleton({ label }: { label: string }) {
  return (
    <div className="space-y-4 p-6" role="status" aria-label={label}>
      <Skeleton width="w-full" height="h-4" count={3} />
    </div>
  )
}

function InlineError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div role="alert" className="flex flex-wrap items-center justify-between gap-3 px-6 py-5 text-sm text-status-danger">
      <span>{message}</span>
      <button
        type="button"
        onClick={onRetry}
        className="rounded-control border border-border-light px-3 py-1.5 font-medium text-text-primary hover:bg-surface-hover transition-colors"
      >
        Tentar novamente
      </button>
    </div>
  )
}

function ConversationRow({
  conversation,
  firstName,
  onOpen,
}: {
  conversation: ContactConversation
  firstName: string
  onOpen: () => void
}) {
  const open = conversation.status === 'open'
  const closed = conversation.status === 'closed'
  return (
    <li>
      <button
        type="button"
        onClick={onOpen}
        className="flex w-full flex-col gap-2 px-6 py-4 text-left transition-colors hover:bg-surface-hover sm:flex-row sm:items-center sm:gap-4"
      >
        <div className="min-w-0 flex-1 space-y-1.5">
          <ChannelChip channel={conversation.channel} />
          <p className="truncate text-sm text-text-secondary">
            {conversationPreview(conversation.last_message, firstName)}
          </p>
        </div>
        <div className="flex shrink-0 flex-col gap-1 text-xs text-text-secondary sm:items-end">
          <span className="tabular-nums">
            {formatInteraction(conversation.last_message?.created_at ?? conversation.updated_at)}
          </span>
          <span className="flex items-center gap-2">
            <Badge size="sm" variant={open ? 'success' : 'default'}>
              {open ? 'Aberta' : closed ? 'Encerrada' : conversation.status}
            </Badge>
            <span className="tabular-nums">{messageCountLabel(conversation.message_count)}</span>
          </span>
        </div>
      </button>
    </li>
  )
}

function TicketList({ tickets, onOpen }: { tickets: ContactTicket[]; onOpen: (conversationId: string) => void }) {
  const subject = (t: ContactTicket) => (
    <>
      {t.external_ticket_id && (
        <span className="mr-1.5 font-mono text-xs text-text-tertiary">#{t.external_ticket_id}</span>
      )}
      {t.subject.trim() ? t.subject : <span className="text-text-tertiary">Sem assunto</span>}
      {t.customer_account_name && <span className="ml-2 text-xs text-text-tertiary">· {t.customer_account_name}</span>}
    </>
  )
  return (
    <>
      <div className="hidden md:block">
        <Table className="rounded-none border-0">
          <TableHead>
            <TableRow>
              <TableHeaderCell>Assunto</TableHeaderCell>
              <TableHeaderCell>Status</TableHeaderCell>
              <TableHeaderCell>Prioridade</TableHeaderCell>
              <TableHeaderCell>Atualização</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {tickets.map((t) => (
              <TableRow key={t.id} interactive onClick={() => onOpen(t.conversation_id)}>
                <TableCell className="max-w-xs truncate text-text-primary">{subject(t)}</TableCell>
                <TableCell>
                  <StatusBadge status={TICKET_STATUS[t.status].tone} size="sm">
                    {TICKET_STATUS[t.status].label}
                  </StatusBadge>
                </TableCell>
                <TableCell className="text-text-secondary">{TICKET_PRIORITY[t.priority]}</TableCell>
                <TableCell className="whitespace-nowrap text-text-secondary">{formatInteraction(t.updated_at)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <ul className="divide-y divide-border-subtle md:hidden">
        {tickets.map((t) => (
          <li key={t.id}>
            <button
              type="button"
              onClick={() => onOpen(t.conversation_id)}
              className="w-full px-6 py-4 text-left hover:bg-surface-hover transition-colors"
            >
              <p className="text-sm text-text-primary">{subject(t)}</p>
              <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-text-secondary">
                <StatusBadge status={TICKET_STATUS[t.status].tone} size="sm">
                  {TICKET_STATUS[t.status].label}
                </StatusBadge>
                <span>{TICKET_PRIORITY[t.priority]}</span>
                <span className="ml-auto">{formatInteraction(t.updated_at)}</span>
              </div>
            </button>
          </li>
        ))}
      </ul>
    </>
  )
}

function ProfileCard({ contact }: { contact: Contact }) {
  return (
    <section className="h-fit rounded-card border border-border-subtle bg-surface" aria-label="Perfil do contato">
      <h2 className="border-b border-border-subtle px-6 py-4 text-section-sm font-semibold text-text-primary">
        Perfil do contato
      </h2>
      <dl className="divide-y divide-border-subtle">
        <CopyableField label="Telefone" value={formatPhone(contact.phone_e164)} copyValue={contact.phone_e164} />
        {contact.email ? (
          <CopyableField label="E-mail" value={contact.email} copyValue={contact.email} />
        ) : (
          <Field label="E-mail" value="Não informado" muted />
        )}
        {/* O status já está no cabeçalho e "Cliente desde" também; repetir polui. */}
        <Field label="Atualizado em" value={formatDateTime(contact.updated_at)} />
      </dl>
    </section>
  )
}

function Field({ label, value, muted = false }: { label: string; value: string; muted?: boolean }) {
  return (
    <div className="flex flex-col gap-1 px-6 py-4">
      <dt className="text-sm text-text-secondary">{label}</dt>
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
    <div className="flex flex-col gap-1 px-6 py-4">
      <dt className="text-sm text-text-secondary">{label}</dt>
      <dd className="flex items-center gap-2 text-text-primary">
        <span className="min-w-0 truncate tabular-nums">{value}</span>
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
