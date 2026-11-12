import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button } from '../primitives'
import { useDebounced } from '../../hooks/useDebounced'
import { accountsAPI } from '../../lib/accounts'
import { accountDirectoryAPI } from '../../lib/contacts'
import { groupErrorMessage, groupsAPI, type Group, type GroupAccountRef } from '../../lib/groups'
import { getTenantId } from '../../lib/session'

interface Props {
  group: Group
  canManage: boolean
}

type Option = { key: string; label: string; detail?: string; ref: GroupAccountRef }

// Which search panel is open, if any: 'primary' edits the group's ONE main client (account_id); 'additional'
// adds one more client to the set below it (ADR-0059 addendum 8 — e.g. a "swap"/peering group between two
// of the tenant's own clients, where the tenant operates on behalf of both). Both share the same merged
// search (CRM directory + local accounts) and panel UI; only the mutation they call differs.
type Mode = 'closed' | 'primary' | 'additional'

// "Cliente final" of a group (product proposal: prestadora = tenant, cliente final = customer_accounts,
// ADR-0018). Reuses the account registry already built for contacts/tickets, AND the tenant's own CRM
// directory (ADR-0059 addendum 3): a groups-only tenant (e.g. K3G_Grupos, run alongside K3G_Solutions while
// groups is built) has no contacts/tickets to ever have created a local account, so customer_accounts starts
// empty — searching only local accounts would leave it with nothing to pick. The two sources are merged into
// one search, same as the contact classification picker (ContactKindControl's CompanyPicker).
export function GroupAccountLink({ group, canManage }: Props) {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<Mode>('closed')
  const [query, setQuery] = useState('')
  const [error, setError] = useState<string | null>(null)
  const debouncedQuery = useDebounced(query, 300)
  const open = mode !== 'closed'

  const search = useQuery({
    queryKey: ['group-account-search', tenantId, debouncedQuery],
    queryFn: () => accountsAPI.list({ q: debouncedQuery }),
    enabled: open,
  })
  // The CRM directory has no server-side query: it is a short, complete list of active companies, fetched
  // once and filtered by the same text locally (same approach as ContactKindControl's CompanyPicker).
  const directory = useQuery({
    queryKey: ['group-account-directory', tenantId],
    queryFn: () => accountDirectoryAPI.directory(),
    enabled: open,
    retry: false,
  })

  // Already-linked accounts (primary + additional) never need to be offered again — adding the primary as
  // additional is refused server-side anyway (422), and re-adding an existing additional is a no-op, but
  // hiding both keeps the list meaningfully short.
  const alreadyLinked = useMemo(() => {
    const ids = new Set(group.additional_accounts.map((a) => a.id))
    if (group.account_id) ids.add(group.account_id)
    return ids
  }, [group.account_id, group.additional_accounts])

  const options = useMemo<Option[]>(() => {
    const dirItems = directory.data?.items ?? []
    const seen = new Set(dirItems.map((c) => c.name.trim().toLowerCase()))
    const needle = query.trim().toLowerCase()
    const dir = dirItems
      .filter((c) => !needle || c.name.toLowerCase().includes(needle) || (c.cnpj ?? '').includes(needle))
      .map((c): Option => ({ key: `d:${c.id}`, label: c.name, detail: c.cnpj ?? 'CRM', ref: { directoryCompanyId: c.id } }))
    // local accounts already shown through a matching directory entry are not repeated; the backend search (q)
    // already narrows this list, so no extra client-side filtering is needed here.
    const local = (search.data ?? [])
      .filter((a) => !seen.has(a.name.trim().toLowerCase()))
      .map((a): Option => ({ key: `a:${a.id}`, label: a.name, detail: 'Conta local', ref: { accountId: a.id } }))
    return [...dir, ...local].filter((o) => !('accountId' in o.ref && alreadyLinked.has(o.ref.accountId)))
  }, [directory.data, search.data, query, alreadyLinked])

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['groups-list', tenantId] })
    void queryClient.invalidateQueries({ queryKey: ['group-account-search', tenantId] })
  }
  const close = () => {
    setMode('closed')
    setQuery('')
  }
  const link = useMutation({
    mutationFn: (ref: GroupAccountRef) => (mode === 'additional' ? groupsAPI.addAdditionalAccount(group.id, ref) : groupsAPI.linkAccount(group.id, ref)),
    onSuccess: () => {
      invalidate()
      close()
    },
    onError: (err) => setError(groupErrorMessage(err)),
  })
  const unlink = useMutation({
    mutationFn: () => groupsAPI.unlinkAccount(group.id),
    onSuccess: invalidate,
    onError: (err) => setError(groupErrorMessage(err)),
  })
  const removeAdditional = useMutation({
    mutationFn: (accountId: string) => groupsAPI.removeAdditionalAccount(group.id, accountId),
    onSuccess: invalidate,
    onError: (err) => setError(groupErrorMessage(err)),
  })

  if (!open) {
    return (
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-1.5">
          {group.account_name ? (
            <Badge variant="info" size="sm">
              {group.account_name}
            </Badge>
          ) : (
            <span className="text-xs text-text-tertiary">Sem cliente vinculado</span>
          )}
          {canManage && (
            <button type="button" onClick={() => setMode('primary')} className="text-xs font-medium text-accent-primary hover:underline">
              {group.account_name ? 'Alterar' : 'Vincular'}
            </button>
          )}
          {canManage && group.account_name && (
            <button type="button" disabled={unlink.isPending} onClick={() => unlink.mutate()} className="text-xs text-text-tertiary hover:underline">
              Remover
            </button>
          )}
        </div>
        {group.additional_accounts.length > 0 && (
          <ul className="flex flex-wrap items-center gap-1.5">
            {group.additional_accounts.map((a) => (
              <li key={a.id} className="flex items-center gap-1">
                <Badge variant="default" size="sm">
                  {a.name}
                </Badge>
                {canManage && (
                  <button
                    type="button"
                    disabled={removeAdditional.isPending}
                    onClick={() => removeAdditional.mutate(a.id)}
                    className="text-[11px] text-text-tertiary hover:underline"
                  >
                    Remover
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
        {canManage && (
          <button type="button" onClick={() => setMode('additional')} className="self-start text-[11px] font-medium text-accent-primary hover:underline">
            + Adicionar outro cliente
          </button>
        )}
        {error && <span className="text-xs text-status-danger">{error}</span>}
      </div>
    )
  }

  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex items-center gap-1.5">
        <input
          autoFocus
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Buscar cliente final..."
          aria-label="Buscar cliente final"
          className="min-w-0 flex-1 rounded-control border border-border-subtle bg-surface px-2 py-1 text-xs text-text-primary focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary"
        />
        <Button size="sm" variant="tertiary" onClick={close}>
          Cancelar
        </Button>
      </div>
      {directory.isError && (
        <p className="text-xs text-status-warning">Diretório de empresas do CRM indisponível: só contas já existentes aparecem.</p>
      )}
      <ul className="max-h-40 overflow-y-auto rounded-control border border-border-subtle bg-surface" role="listbox" aria-label="Empresas">
        {search.isLoading || directory.isLoading ? (
          <li className="px-2 py-1.5 text-xs text-text-tertiary">Carregando...</li>
        ) : options.length === 0 ? (
          <li className="px-2 py-1.5 text-xs text-text-tertiary">Nenhum cliente encontrado.</li>
        ) : (
          options.map((o) => (
            <li key={o.key} role="option">
              <button
                type="button"
                disabled={link.isPending}
                onClick={() => link.mutate(o.ref)}
                className="flex w-full items-center justify-between gap-2 truncate px-2 py-1.5 text-left text-xs text-text-primary hover:bg-surface-muted"
              >
                <span className="truncate">{o.label}</span>
                {o.detail && <span className="shrink-0 text-text-tertiary">{o.detail}</span>}
              </button>
            </li>
          ))
        )}
      </ul>
      {error && (
        <p role="alert" className="text-xs text-status-danger">
          {error}
        </p>
      )}
    </div>
  )
}
