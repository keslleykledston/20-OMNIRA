import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import {
  EmptyState,
  ErrorState,
  FilterBar,
  Icon,
  PageHeader,
  Pagination,
  PermissionState,
  StatusBadge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  Tabs,
  type TabItem,
} from '../components/primitives'
import {
  reconciliationAPI,
  reconciliationErrorMessage,
  type AttemptState,
  type CreateAttemptItem,
  type ReconciliationFilters,
  type StatusAttemptItem,
} from '../lib/reconciliation'
import { providerStatusOptions } from '../lib/tickets'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'

const PAGE_SIZE = 20

const STATE_LABELS: Record<AttemptState, string> = {
  in_flight: 'Em andamento',
  outcome_unknown: 'Verificação necessária',
  confirmed_failure: 'Falha confirmada',
  confirmed_success: 'Confirmado',
}

function formatDate(iso: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString('pt-BR', { day: '2-digit', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })
}

// Presentation only — never a computed replacement for the durable backend
// state (section 7): the badge tone/label always derives from the exact
// state string the API returned, plus projection_synced_at for the one
// documented confirmed_success split.
function operationalState(state: AttemptState, projectionSyncedAt: string | null, createdAt: string): { label: string; tone: 'success' | 'warning' | 'danger' | 'default' } {
  switch (state) {
    case 'outcome_unknown':
      return { label: 'Verificação necessária', tone: 'warning' }
    case 'confirmed_failure':
      return { label: 'Falha confirmada', tone: 'danger' }
    case 'confirmed_success':
      return projectionSyncedAt
        ? { label: 'Confirmado / sincronizado', tone: 'success' }
        : { label: 'Confirmado / projeção pendente', tone: 'warning' }
    case 'in_flight':
    default:
      return { label: `Em andamento há ${formatAge(createdAt)}`, tone: 'default' }
  }
}

// Presentation-only elapsed time — no TTL, no automatic transition, purely
// informational (section 8).
function formatAge(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  const minutes = Math.max(0, Math.floor(ms / 60000))
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

// PRODUCT.6-O2BF's provider-status registry, reused here ONLY as a
// presentation lookup for an opaque target_status code (section 6) —
// never a universal OMNIRA lifecycle enum, never sent back to any API.
function providerStatusLabel(provider: string, code: string): string {
  const options = providerStatusOptions(provider)
  return options?.find((o) => o.code === code)?.label ?? code
}

function ActorCell({ actor }: { actor: { display_name: string; email: string } }) {
  const label = actor.display_name || actor.email || '—'
  return <span className="text-text-secondary">{label}</span>
}

function OpenConversationAction({ conversationId }: { conversationId: string }) {
  return (
    <Link
      to={`/inbox?conversation_id=${encodeURIComponent(conversationId)}`}
      className="inline-flex items-center rounded-control px-2 py-1 text-xs font-medium text-accent-primary hover:bg-accent-primary-soft focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary"
    >
      Abrir conversa
    </Link>
  )
}

type TabId = 'create' | 'status'

function FilterControls({ filters, onChange }: { filters: ReconciliationFilters; onChange: (patch: Partial<ReconciliationFilters>) => void }) {
  return (
    <FilterBar>
      <select
        aria-label="Filtrar por estado"
        value={filters.state ?? ''}
        onChange={(e) => onChange({ state: (e.target.value || undefined) as AttemptState | undefined })}
        className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm"
      >
        <option value="">Todos os estados</option>
        {(Object.keys(STATE_LABELS) as AttemptState[]).map((s) => (
          <option key={s} value={s}>{STATE_LABELS[s]}</option>
        ))}
      </select>
      <input
        aria-label="Filtrar por provedor"
        placeholder="Provedor"
        value={filters.provider ?? ''}
        onChange={(e) => onChange({ provider: e.target.value || undefined })}
        className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm"
      />
      <input
        aria-label="Filtrar por ID externo"
        placeholder="ID do chamado externo"
        value={filters.externalTicketId ?? ''}
        onChange={(e) => onChange({ externalTicketId: e.target.value || undefined })}
        className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm"
      />
      <input
        aria-label="Data inicial"
        type="date"
        value={filters.createdFrom ? filters.createdFrom.slice(0, 10) : ''}
        onChange={(e) => onChange({ createdFrom: e.target.value ? `${e.target.value}T00:00:00Z` : undefined })}
        className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm"
      />
      <input
        aria-label="Data final"
        type="date"
        value={filters.createdTo ? filters.createdTo.slice(0, 10) : ''}
        onChange={(e) => onChange({ createdTo: e.target.value ? `${e.target.value}T23:59:59Z` : undefined })}
        className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm"
      />
    </FilterBar>
  )
}

export default function TicketReconciliationPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const canView = access.can('ticket.reconcile')

  const [activeTab, setActiveTab] = useState<TabId>('create')
  const tabItems: TabItem<TabId>[] = [
    { id: 'create', label: 'Criação' },
    { id: 'status', label: 'Status' },
  ]

  // Each tab keeps its own cursor stack and filters — never shared, never
  // mixed (section 13).
  const [createCursorStack, setCreateCursorStack] = useState<string[]>([])
  const [createFilters, setCreateFilters] = useState<ReconciliationFilters>({})
  const createCursor = createCursorStack[createCursorStack.length - 1]

  const [statusCursorStack, setStatusCursorStack] = useState<string[]>([])
  const [statusFilters, setStatusFilters] = useState<ReconciliationFilters>({})
  const statusCursor = statusCursorStack[statusCursorStack.length - 1]

  const createQuery = useQuery({
    queryKey: [
      'ticket-reconciliation-create', tenantId, createCursor ?? 'first',
      createFilters.state ?? '', createFilters.provider ?? '', createFilters.externalTicketId ?? '',
      createFilters.createdFrom ?? '', createFilters.createdTo ?? '',
    ],
    queryFn: () => reconciliationAPI.listCreateAttempts(createCursor, PAGE_SIZE, createFilters),
    enabled: canView && activeTab === 'create',
    retry: false,
  })

  const statusQuery = useQuery({
    queryKey: [
      'ticket-reconciliation-status', tenantId, statusCursor ?? 'first',
      statusFilters.state ?? '', statusFilters.provider ?? '', statusFilters.externalTicketId ?? '',
      statusFilters.createdFrom ?? '', statusFilters.createdTo ?? '',
    ],
    queryFn: () => reconciliationAPI.listStatusAttempts(statusCursor, PAGE_SIZE, statusFilters),
    enabled: canView && activeTab === 'status',
    retry: false,
  })

  const updateCreateFilter = (patch: Partial<ReconciliationFilters>) => {
    setCreateCursorStack([])
    setCreateFilters((prev) => ({ ...prev, ...patch }))
  }
  const updateStatusFilter = (patch: Partial<ReconciliationFilters>) => {
    setStatusCursorStack([])
    setStatusFilters((prev) => ({ ...prev, ...patch }))
  }

  const createItems = createQuery.data?.items ?? []
  const statusItems = statusQuery.data?.items ?? []

  return (
    <div className="space-y-6">
      <PageHeader
        title="Reconciliação de chamados"
        description="Visibilidade somente leitura das tentativas de integração de chamados externos — nenhuma ação de mutação é executada aqui."
      />

      {!access.isLoading && !canView && (
        <PermissionState message="Você não tem permissão para visualizar a reconciliação de chamados." />
      )}

      {canView && (
        <>
          <Tabs items={tabItems} value={activeTab} onChange={setActiveTab} aria-label="Tipo de operação" />

          {activeTab === 'create' && (
            <div className="space-y-4">
              <FilterControls filters={createFilters} onChange={updateCreateFilter} />

              {createQuery.isError && (
                <ErrorState
                  message={reconciliationErrorMessage(createQuery.error)}
                  action={{ label: 'Tentar novamente', onClick: () => void createQuery.refetch() }}
                />
              )}

              {createQuery.isLoading && <p className="text-sm text-text-secondary" aria-busy="true">Carregando…</p>}

              {!createQuery.isLoading && !createQuery.isError && createItems.length === 0 && (
                <EmptyState
                  icon={<Icon name="tickets" />}
                  title={createCursorStack.length > 0 ? 'Nada nesta página' : 'Nenhuma tentativa de criação'}
                  description={
                    createCursorStack.length > 0
                      ? 'Volte para a página anterior.'
                      : 'Nenhuma tentativa de criação de chamado externo corresponde aos filtros atuais.'
                  }
                  action={
                    createCursorStack.length > 0
                      ? { label: 'Voltar', onClick: () => setCreateCursorStack((s) => s.slice(0, -1)) }
                      : undefined
                  }
                />
              )}

              {!createQuery.isLoading && !createQuery.isError && createItems.length > 0 && (
                <>
                  <div className="hidden md:block">
                    <Table>
                      <TableHead>
                        <TableRow>
                          <TableHeaderCell>Estado</TableHeaderCell>
                          <TableHeaderCell>Chamado</TableHeaderCell>
                          <TableHeaderCell>Provedor</TableHeaderCell>
                          <TableHeaderCell>ID externo</TableHeaderCell>
                          <TableHeaderCell>Ator</TableHeaderCell>
                          <TableHeaderCell>Criado em</TableHeaderCell>
                          <TableHeaderCell>Chave</TableHeaderCell>
                          <TableHeaderCell>Ação</TableHeaderCell>
                        </TableRow>
                      </TableHead>
                      <TableBody>
                        {createItems.map((item: CreateAttemptItem) => {
                          const op = operationalState(item.state, item.projection_synced_at, item.created_at)
                          return (
                            <TableRow key={item.id}>
                              <TableCell>
                                <StatusBadge status={op.tone} size="sm">{op.label}</StatusBadge>
                              </TableCell>
                              <TableCell className="text-text-secondary">{item.local_ticket_subject ?? '—'}</TableCell>
                              <TableCell className="text-text-secondary">{item.provider ?? '—'}</TableCell>
                              <TableCell className="text-text-secondary">{item.external_ticket_id ?? '—'}</TableCell>
                              <TableCell><ActorCell actor={item.actor} /></TableCell>
                              <TableCell className="text-text-secondary">{formatDate(item.created_at)}</TableCell>
                              <TableCell className="font-mono text-text-tertiary text-xs">{item.idempotency_key_redacted}</TableCell>
                              <TableCell><OpenConversationAction conversationId={item.conversation_id} /></TableCell>
                            </TableRow>
                          )
                        })}
                      </TableBody>
                    </Table>
                  </div>

                  <div className="md:hidden space-y-3">
                    {createItems.map((item: CreateAttemptItem) => {
                      const op = operationalState(item.state, item.projection_synced_at, item.created_at)
                      return (
                        <div key={item.id} className="rounded-card border border-border-subtle bg-surface p-4">
                          <StatusBadge status={op.tone} size="sm">{op.label}</StatusBadge>
                          <p className="mt-2 font-medium text-text-primary">{item.local_ticket_subject ?? '(sem ticket local)'}</p>
                          <p className="mt-1 text-sm text-text-secondary">{item.provider ?? '—'} {item.external_ticket_id ? `#${item.external_ticket_id}` : ''}</p>
                          <div className="mt-2 flex items-center justify-between">
                            <ActorCell actor={item.actor} />
                            <OpenConversationAction conversationId={item.conversation_id} />
                          </div>
                        </div>
                      )
                    })}
                  </div>

                  <Pagination
                    hasPrevious={createCursorStack.length > 0}
                    hasNext={Boolean(createQuery.data?.has_more && createQuery.data?.next_cursor)}
                    onPrevious={() => setCreateCursorStack((s) => s.slice(0, -1))}
                    onNext={() => createQuery.data?.next_cursor && setCreateCursorStack((s) => [...s, createQuery.data!.next_cursor!])}
                    label={`${createQuery.data?.count ?? 0} ${createQuery.data?.count === 1 ? 'tentativa' : 'tentativas'} nesta página`}
                  />
                </>
              )}
            </div>
          )}

          {activeTab === 'status' && (
            <div className="space-y-4">
              <FilterControls filters={statusFilters} onChange={updateStatusFilter} />

              {statusQuery.isError && (
                <ErrorState
                  message={reconciliationErrorMessage(statusQuery.error)}
                  action={{ label: 'Tentar novamente', onClick: () => void statusQuery.refetch() }}
                />
              )}

              {statusQuery.isLoading && <p className="text-sm text-text-secondary" aria-busy="true">Carregando…</p>}

              {!statusQuery.isLoading && !statusQuery.isError && statusItems.length === 0 && (
                <EmptyState
                  icon={<Icon name="tickets" />}
                  title={statusCursorStack.length > 0 ? 'Nada nesta página' : 'Nenhuma tentativa de status'}
                  description={
                    statusCursorStack.length > 0
                      ? 'Volte para a página anterior.'
                      : 'Nenhuma tentativa de mutação de status corresponde aos filtros atuais.'
                  }
                  action={
                    statusCursorStack.length > 0
                      ? { label: 'Voltar', onClick: () => setStatusCursorStack((s) => s.slice(0, -1)) }
                      : undefined
                  }
                />
              )}

              {!statusQuery.isLoading && !statusQuery.isError && statusItems.length > 0 && (
                <>
                  <div className="hidden md:block">
                    <Table>
                      <TableHead>
                        <TableRow>
                          <TableHeaderCell>Estado</TableHeaderCell>
                          <TableHeaderCell>Chamado</TableHeaderCell>
                          <TableHeaderCell>Provedor</TableHeaderCell>
                          <TableHeaderCell>Alvo</TableHeaderCell>
                          <TableHeaderCell>Resultado confirmado</TableHeaderCell>
                          <TableHeaderCell>Ator</TableHeaderCell>
                          <TableHeaderCell>Criado em</TableHeaderCell>
                          <TableHeaderCell>Chave</TableHeaderCell>
                          <TableHeaderCell>Ação</TableHeaderCell>
                        </TableRow>
                      </TableHead>
                      <TableBody>
                        {statusItems.map((item: StatusAttemptItem) => {
                          const op = operationalState(item.state, item.projection_synced_at, item.created_at)
                          const confirmed = item.confirmed_external_status_label
                            ?? (item.confirmed_external_status ? providerStatusLabel(item.provider, item.confirmed_external_status) : null)
                          return (
                            <TableRow key={item.id}>
                              <TableCell>
                                <StatusBadge status={op.tone} size="sm">{op.label}</StatusBadge>
                              </TableCell>
                              <TableCell className="text-text-secondary">{item.local_ticket_subject ?? '—'} {item.external_ticket_id ? `#${item.external_ticket_id}` : ''}</TableCell>
                              <TableCell className="text-text-secondary">{item.provider}</TableCell>
                              <TableCell className="text-text-secondary">{providerStatusLabel(item.provider, item.target_status)}</TableCell>
                              <TableCell className="text-text-secondary">{confirmed ?? '—'}</TableCell>
                              <TableCell><ActorCell actor={item.actor} /></TableCell>
                              <TableCell className="text-text-secondary">{formatDate(item.created_at)}</TableCell>
                              <TableCell className="font-mono text-text-tertiary text-xs">{item.idempotency_key_redacted}</TableCell>
                              <TableCell><OpenConversationAction conversationId={item.conversation_id} /></TableCell>
                            </TableRow>
                          )
                        })}
                      </TableBody>
                    </Table>
                  </div>

                  <div className="md:hidden space-y-3">
                    {statusItems.map((item: StatusAttemptItem) => {
                      const op = operationalState(item.state, item.projection_synced_at, item.created_at)
                      const confirmed = item.confirmed_external_status_label
                        ?? (item.confirmed_external_status ? providerStatusLabel(item.provider, item.confirmed_external_status) : null)
                      return (
                        <div key={item.id} className="rounded-card border border-border-subtle bg-surface p-4">
                          <StatusBadge status={op.tone} size="sm">{op.label}</StatusBadge>
                          <p className="mt-2 font-medium text-text-primary">{item.local_ticket_subject ?? '(sem assunto)'} #{item.external_ticket_id}</p>
                          <p className="mt-1 text-sm text-text-secondary">Alvo: {providerStatusLabel(item.provider, item.target_status)}</p>
                          <p className="mt-1 text-sm text-text-secondary">Confirmado: {confirmed ?? '—'}</p>
                          <div className="mt-2 flex items-center justify-between">
                            <ActorCell actor={item.actor} />
                            <OpenConversationAction conversationId={item.conversation_id} />
                          </div>
                        </div>
                      )
                    })}
                  </div>

                  <Pagination
                    hasPrevious={statusCursorStack.length > 0}
                    hasNext={Boolean(statusQuery.data?.has_more && statusQuery.data?.next_cursor)}
                    onPrevious={() => setStatusCursorStack((s) => s.slice(0, -1))}
                    onNext={() => statusQuery.data?.next_cursor && setStatusCursorStack((s) => [...s, statusQuery.data!.next_cursor!])}
                    label={`${statusQuery.data?.count ?? 0} ${statusQuery.data?.count === 1 ? 'tentativa' : 'tentativas'} nesta página`}
                  />
                </>
              )}
            </div>
          )}
        </>
      )}
    </div>
  )
}
