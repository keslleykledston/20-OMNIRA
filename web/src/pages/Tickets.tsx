import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  Button,
  EmptyState,
  ErrorState,
  FilterBar,
  Icon,
  PageHeader,
  Pagination,
  Skeleton,
  StatusBadge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from '../components/primitives'
import { ticketErrorMessage, ticketsAPI, type Ticket, type TicketFilters } from '../lib/tickets'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'

const PAGE_SIZE = 20

const STATUS_LABELS: Record<Ticket['status'], { label: string; tone: 'success' | 'warning' | 'danger' | 'default' }> = {
  open: { label: 'Aberto', tone: 'danger' },
  in_progress: { label: 'Em progresso', tone: 'warning' },
  waiting: { label: 'Aguardando', tone: 'default' },
  resolved: { label: 'Resolvido', tone: 'success' },
  closed: { label: 'Fechado', tone: 'default' },
}

const PRIORITY_LABELS: Record<Ticket['priority'], string> = {
  critical: 'Crítica',
  high: 'Alta',
  medium: 'Média',
  low: 'Baixa',
}

function formatDate(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString('pt-BR', { day: '2-digit', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export default function TicketsPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const [filters, setFilters] = useState<TicketFilters>({})
  const [exportError, setExportError] = useState<string | null>(null)

  // The API paginates by cursor, so going back is not "page - 1": we keep the
  // cursor that opened each page and pop it to return (same pattern as
  // ContactsPage).
  const [cursorStack, setCursorStack] = useState<string[]>([])
  const cursor = cursorStack[cursorStack.length - 1]

  const tickets = useQuery({
    queryKey: ['tickets', tenantId, cursor ?? 'first', filters.status ?? '', filters.priority ?? ''],
    queryFn: () => ticketsAPI.list(cursor, PAGE_SIZE, filters),
    retry: false,
  })

  const page = tickets.data
  const items = page?.items ?? []

  const updateFilter = (patch: Partial<TicketFilters>) => {
    setCursorStack([])
    setFilters((prev) => ({ ...prev, ...patch }))
  }

  // Exports the full filtered result (server-bounded), never just the
  // currently visible pagination page.
  const exportMutation = useMutation({
    mutationFn: () => ticketsAPI.exportCSV(filters),
    onSuccess: ({ blob, filename }) => {
      setExportError(null)
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = filename
      document.body.appendChild(link)
      link.click()
      document.body.removeChild(link)
      URL.revokeObjectURL(url)
    },
    onError: (err) => setExportError(ticketErrorMessage(err, 'Não foi possível exportar os tickets')),
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title="Tickets"
        description="Solicitações de suporte vinculadas às conversas do Inbox."
        actions={
          access.can('ticket.read') && (
            <Button variant="secondary" size="md" isLoading={exportMutation.isPending} onClick={() => exportMutation.mutate()}>
              <Icon name="reports" size={18} />
              Exportar CSV
            </Button>
          )
        }
      />

      {exportError && (
        <ErrorState message={exportError} isDismissible onDismiss={() => setExportError(null)} />
      )}

      <FilterBar>
        <select
          aria-label="Filtrar por status"
          value={filters.status ?? ''}
          onChange={(e) => updateFilter({ status: (e.target.value || undefined) as Ticket['status'] | undefined })}
          className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm"
        >
          <option value="">Todos os status</option>
          {Object.entries(STATUS_LABELS).map(([value, { label }]) => (
            <option key={value} value={value}>{label}</option>
          ))}
        </select>
        <select
          aria-label="Filtrar por prioridade"
          value={filters.priority ?? ''}
          onChange={(e) => updateFilter({ priority: (e.target.value || undefined) as Ticket['priority'] | undefined })}
          className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm"
        >
          <option value="">Todas as prioridades</option>
          {Object.entries(PRIORITY_LABELS).map(([value, label]) => (
            <option key={value} value={value}>{label}</option>
          ))}
        </select>
      </FilterBar>

      {tickets.isError && (
        <ErrorState
          message={ticketErrorMessage(tickets.error)}
          action={{ label: 'Tentar novamente', onClick: () => void tickets.refetch() }}
        />
      )}

      {tickets.isLoading && <TicketsSkeleton />}

      {!tickets.isLoading && !tickets.isError && items.length === 0 && (
        <EmptyState
          icon={<Icon name="tickets" />}
          title={cursorStack.length > 0 ? 'Nada nesta página' : 'Nenhum ticket encontrado'}
          description={
            cursorStack.length > 0
              ? 'Volte para a página anterior.'
              : 'Tickets aparecem aqui conforme surgem a partir das conversas do Inbox.'
          }
          action={
            cursorStack.length > 0
              ? { label: 'Voltar', onClick: () => setCursorStack((s) => s.slice(0, -1)) }
              : undefined
          }
        />
      )}

      {!tickets.isLoading && !tickets.isError && items.length > 0 && (
        <>
          <div className="hidden md:block">
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeaderCell>Assunto</TableHeaderCell>
                  <TableHeaderCell>Prioridade</TableHeaderCell>
                  <TableHeaderCell>Status</TableHeaderCell>
                  <TableHeaderCell>Atualizado</TableHeaderCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {items.map((t) => (
                  <TableRow key={t.id}>
                    <TableCell>
                      <span className="font-medium text-text-primary">{t.subject || '(sem assunto)'}</span>
                    </TableCell>
                    <TableCell className="text-text-secondary">{PRIORITY_LABELS[t.priority]}</TableCell>
                    <TableCell>
                      <StatusBadge status={STATUS_LABELS[t.status].tone} size="sm">
                        {STATUS_LABELS[t.status].label}
                      </StatusBadge>
                    </TableCell>
                    <TableCell className="text-text-secondary">{formatDate(t.updated_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          <div className="md:hidden space-y-3">
            {items.map((t) => (
              <div key={t.id} className="rounded-card border border-border-subtle bg-surface p-4">
                <p className="font-medium text-text-primary">{t.subject || '(sem assunto)'}</p>
                <div className="mt-2 flex flex-wrap items-center gap-2">
                  <span className="text-sm text-text-secondary">{PRIORITY_LABELS[t.priority]}</span>
                  <StatusBadge status={STATUS_LABELS[t.status].tone} size="sm">
                    {STATUS_LABELS[t.status].label}
                  </StatusBadge>
                </div>
                <p className="mt-2 text-xs text-text-tertiary">{formatDate(t.updated_at)}</p>
              </div>
            ))}
          </div>

          <Pagination
            hasPrevious={cursorStack.length > 0}
            hasNext={Boolean(page?.has_more && page?.next_cursor)}
            onPrevious={() => setCursorStack((s) => s.slice(0, -1))}
            onNext={() => page?.next_cursor && setCursorStack((s) => [...s, page.next_cursor!])}
            label={`${page?.count ?? 0} ${page?.count === 1 ? 'ticket' : 'tickets'} nesta página`}
          />
        </>
      )}
    </div>
  )
}

function TicketsSkeleton() {
  return (
    <div className="rounded-card border border-border-subtle bg-surface p-6 space-y-4">
      {Array.from({ length: 6 }).map((_, i) => (
        <Skeleton key={i} width="w-full" height="h-4" />
      ))}
    </div>
  )
}
