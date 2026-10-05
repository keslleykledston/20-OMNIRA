import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { EmptyState, ErrorState, Icon, PageHeader, PermissionState, SearchField, Skeleton, StatusBadge } from '../components/primitives'
import {
  ACCOUNT_STATUS_LABEL,
  ACCOUNT_TYPE_LABEL,
  accountErrorMessage,
  accountsAPI,
  type Account,
  type AccountStatus,
  type AccountType,
} from '../lib/accounts'
import { useDebounced } from '../hooks/useDebounced'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'
import { formatDate } from './ContactsPage'

const FIELD =
  'rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary ' +
  'focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary'

const STATUS_TONE: Record<AccountStatus, 'success' | 'default'> = { active: 'success', inactive: 'default', archived: 'default' }

// Customer accounts (ADR-0018): the organizations the tenant serves. A provider (CRM) company is only a link to one of them
// and is created the first time it is picked for a contact or a ticket; this screen is where they are reviewed and kept.
export default function Accounts() {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const access = useAccess()
  const canRead = access.can('account.read')
  const canManage = access.can('account.manage')
  const canTickets = access.can('ticket.read')
  const [search, setSearch] = useState('')
  const q = useDebounced(search.trim(), 300)
  const [status, setStatus] = useState<AccountStatus | ''>('')
  const [selected, setSelected] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [newType, setNewType] = useState<AccountType>('customer')
  const [error, setError] = useState('')

  const list = useQuery({
    queryKey: ['accounts', tenantId, q, status],
    queryFn: () => accountsAPI.list({ q: q || undefined, status: status || undefined }),
    enabled: canRead,
    retry: false,
  })
  const refresh = () => qc.invalidateQueries({ queryKey: ['accounts', tenantId] })
  const create = useMutation({
    mutationFn: () => accountsAPI.create({ name: newName.trim(), account_type: newType }),
    onSuccess: (a) => {
      setCreating(false)
      setNewName('')
      setError('')
      setSelected(a.id)
      void refresh()
    },
    onError: (e) => setError(accountErrorMessage(e)),
  })

  if (!access.isLoading && !canRead) {
    return (
      <div className="px-6 py-6 lg:px-8 lg:py-8">
        <PermissionState message="Você não tem permissão para ver as empresas." />
      </div>
    )
  }

  const items = list.data ?? []
  return (
    <div className="space-y-6 px-6 py-6 lg:px-8 lg:py-8">
      <PageHeader title="Empresas" description="As organizações que a sua operação atende. Uma empresa do CRM é só um vínculo: ela entra aqui na primeira vez que é escolhida." />

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="sm:max-w-sm sm:flex-1">
          <SearchField aria-label="Buscar empresa" placeholder="Buscar por nome..." value={search} onChange={(e) => setSearch(e.target.value)} onClear={() => setSearch('')} />
        </div>
        <label className="flex items-center gap-2 text-sm text-text-secondary">
          Status
          <select aria-label="Status" className={FIELD} value={status} onChange={(e) => setStatus(e.target.value as AccountStatus | '')}>
            <option value="">Ativas e inativas</option>
            <option value="active">Ativas</option>
            <option value="inactive">Inativas</option>
            <option value="archived">Arquivadas</option>
          </select>
        </label>
        {canManage && (
          <button type="button" onClick={() => setCreating((v) => !v)} className="rounded-control bg-accent-primary px-3 py-2 text-sm font-medium text-white hover:opacity-90">
            Nova empresa
          </button>
        )}
      </div>

      {canManage && creating && (
        <form
          aria-label="Nova empresa"
          className="flex flex-col gap-3 rounded-card border border-border-subtle bg-surface p-4 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault()
            if (newName.trim()) create.mutate()
          }}
        >
          <label className="flex-1 text-sm text-text-secondary">
            Nome
            <input aria-label="Nome da empresa" className={FIELD + ' mt-1 w-full'} maxLength={200} value={newName} onChange={(e) => setNewName(e.target.value)} />
          </label>
          <label className="text-sm text-text-secondary">
            Tipo
            <select aria-label="Tipo da empresa" className={FIELD + ' mt-1 w-full'} value={newType} onChange={(e) => setNewType(e.target.value as AccountType)}>
              {(Object.keys(ACCOUNT_TYPE_LABEL) as AccountType[]).map((t) => (
                <option key={t} value={t}>
                  {ACCOUNT_TYPE_LABEL[t]}
                </option>
              ))}
            </select>
          </label>
          <button type="submit" disabled={!newName.trim() || create.isPending} className="rounded-control bg-accent-primary px-3 py-2 text-sm font-medium text-white disabled:opacity-50">
            Criar
          </button>
        </form>
      )}
      {error && (
        <p role="alert" className="text-sm text-status-danger">
          {error}
        </p>
      )}

      {list.isError && <ErrorState message={accountErrorMessage(list.error, 'Não foi possível carregar as empresas.')} action={{ label: 'Tentar novamente', onClick: () => void list.refetch() }} />}
      {list.isLoading && (
        <div className="space-y-3 rounded-card border border-border-subtle bg-surface p-6">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} width="w-full" height="h-4" />
          ))}
        </div>
      )}
      {!list.isLoading && !list.isError && items.length === 0 && (
        <EmptyState
          icon={<Icon name="info" />}
          title={q || status ? 'Nenhuma empresa encontrada' : 'Nenhuma empresa ainda'}
          description={q || status ? 'Nenhuma empresa combina com a busca e o filtro.' : 'Ao classificar um contato como cliente ou abrir um chamado para uma empresa do CRM, ela aparece aqui.'}
        />
      )}

      {items.length > 0 && (
        <div className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
          <ul className="divide-y divide-border-subtle rounded-card border border-border-subtle bg-surface" aria-label="Empresas">
            {items.map((a) => (
              <li key={a.id}>
                <button
                  type="button"
                  onClick={() => setSelected(selected === a.id ? null : a.id)}
                  aria-pressed={selected === a.id}
                  className={'flex w-full items-center justify-between gap-3 px-4 py-3 text-left hover:bg-surface-muted ' + (selected === a.id ? 'bg-accent-primary-soft' : '')}
                >
                  <span className="min-w-0">
                    <span className="block truncate font-medium text-text-primary">{a.name}</span>
                    <span className="block text-xs text-text-tertiary">
                      {ACCOUNT_TYPE_LABEL[a.account_type]} · desde {formatDate(a.created_at)}
                    </span>
                  </span>
                  <StatusBadge status={STATUS_TONE[a.status]} size="sm">
                    {ACCOUNT_STATUS_LABEL[a.status]}
                  </StatusBadge>
                </button>
              </li>
            ))}
          </ul>
          {selected && <AccountDetail id={selected} canManage={canManage} canTickets={canTickets} onChanged={() => void refresh()} />}
        </div>
      )}
    </div>
  )
}

function AccountDetail({ id, canManage, canTickets, onChanged }: { id: string; canManage: boolean; canTickets: boolean; onChanged: () => void }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [rename, setRename] = useState<string | null>(null)
  const [error, setError] = useState('')
  const key = ['account', tenantId, id]
  const account = useQuery({ queryKey: key, queryFn: () => accountsAPI.get(id), retry: false })
  const tickets = useQuery({ queryKey: ['account-tickets', tenantId, id], queryFn: () => accountsAPI.tickets(id), enabled: canTickets, retry: false })
  const update = useMutation({
    mutationFn: (body: Parameters<typeof accountsAPI.update>[1]) => accountsAPI.update(id, body),
    onSuccess: () => {
      setError('')
      setRename(null)
      void qc.invalidateQueries({ queryKey: key })
      onChanged()
    },
    onError: (e) => setError(accountErrorMessage(e)),
  })

  if (account.isLoading) return <Skeleton width="w-full" height="h-40" />
  if (account.isError || !account.data) return <ErrorState message={accountErrorMessage(account.error, 'Não foi possível carregar a empresa.')} />
  const a: Account = account.data
  return (
    <section aria-label={`Empresa ${a.name}`} className="space-y-5 rounded-card border border-border-subtle bg-surface p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        {rename === null ? (
          <h2 className="text-section-sm font-semibold text-text-primary break-words">{a.name}</h2>
        ) : (
          <form
            className="flex flex-1 gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              if (rename.trim()) update.mutate({ name: rename.trim() })
            }}
          >
            <input aria-label="Novo nome" className={FIELD + ' flex-1'} maxLength={200} value={rename} onChange={(e) => setRename(e.target.value)} />
            <button type="submit" disabled={!rename.trim() || update.isPending} className="rounded-control bg-accent-primary px-3 py-2 text-sm font-medium text-white disabled:opacity-50">
              Salvar
            </button>
            <button type="button" onClick={() => setRename(null)} className="rounded-control border border-border-subtle px-3 py-2 text-sm">
              Cancelar
            </button>
          </form>
        )}
        <StatusBadge status={STATUS_TONE[a.status]}>{ACCOUNT_STATUS_LABEL[a.status]}</StatusBadge>
      </div>

      {canManage && rename === null && (
        <div className="flex flex-wrap gap-2 text-sm">
          <button type="button" onClick={() => setRename(a.name)} className="rounded-control border border-border-subtle px-3 py-1.5 hover:bg-surface-muted">
            Renomear
          </button>
          {a.status === 'active' && (
            <button type="button" disabled={update.isPending} onClick={() => update.mutate({ status: 'inactive' })} className="rounded-control border border-border-subtle px-3 py-1.5 hover:bg-surface-muted">
              Inativar
            </button>
          )}
          {a.status !== 'active' && (
            <button type="button" disabled={update.isPending} onClick={() => update.mutate({ status: 'active' })} className="rounded-control border border-border-subtle px-3 py-1.5 hover:bg-surface-muted">
              {a.status === 'archived' ? 'Restaurar' : 'Reativar'}
            </button>
          )}
          {a.status !== 'archived' && (
            <button type="button" disabled={update.isPending} onClick={() => update.mutate({ status: 'archived' })} className="rounded-control border border-status-danger-border px-3 py-1.5 text-status-danger hover:bg-status-danger-soft">
              Arquivar
            </button>
          )}
        </div>
      )}
      {error && (
        <p role="alert" className="text-sm text-status-danger">
          {error}
        </p>
      )}

      <div>
        <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-text-tertiary">Vínculos com o CRM</h3>
        {(a.external_links ?? []).length === 0 ? (
          <p className="text-sm text-text-secondary">Nenhuma empresa do CRM vinculada. Esta empresa existe só no OMNIRA.</p>
        ) : (
          <ul className="space-y-1.5 text-sm">
            {a.external_links!.map((l) => (
              <li key={l.id} className="flex flex-wrap items-center gap-2">
                <span className="font-medium text-text-primary">{l.external_name_snapshot ?? `Empresa ${l.external_company_id}`}</span>
                <span className="text-xs text-text-tertiary">
                  {l.provider} · id {l.external_company_id}
                </span>
                <StatusBadge status={l.status === 'active' ? 'success' : 'default'} size="sm">
                  {l.status === 'active' ? 'ativo' : 'inativo no CRM'}
                </StatusBadge>
              </li>
            ))}
          </ul>
        )}
      </div>

      {canTickets && (
        <div>
          <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-text-tertiary">Chamados</h3>
          {tickets.isLoading && <Skeleton width="w-full" height="h-4" />}
          {tickets.isError && <p className="text-sm text-text-secondary">Não foi possível carregar os chamados.</p>}
          {tickets.data && tickets.data.length === 0 && <p className="text-sm text-text-secondary">Nenhum chamado para esta empresa.</p>}
          <ul className="space-y-1.5 text-sm">
            {(tickets.data ?? []).map((t) => (
              <li key={t.id} className="flex flex-wrap items-center gap-2">
                {t.external_ticket_id && <span className="font-mono text-xs text-text-tertiary">#{t.external_ticket_id}</span>}
                <span className="text-text-primary">{t.subject.trim() || 'Sem assunto'}</span>
                <span className="text-xs text-text-tertiary">{t.status}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}
