import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button } from '../primitives'
import { useDebounced } from '../../hooks/useDebounced'
import { accountsAPI } from '../../lib/accounts'
import { groupErrorMessage, groupsAPI, type Group } from '../../lib/groups'
import { getTenantId } from '../../lib/session'

interface Props {
  group: Group
  canManage: boolean
}

// "Cliente final" of a group (product proposal: prestadora = tenant, cliente final = customer_accounts,
// ADR-0018). Reuses the account registry already built for contacts/tickets — searches it instead of
// letting a group carry a free-text company name.
export function GroupAccountLink({ group, canManage }: Props) {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [query, setQuery] = useState('')
  const [error, setError] = useState<string | null>(null)
  const debouncedQuery = useDebounced(query, 300)

  const search = useQuery({
    queryKey: ['group-account-search', tenantId, debouncedQuery],
    queryFn: () => accountsAPI.list({ q: debouncedQuery }),
    enabled: editing,
  })

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['groups-list', tenantId] })
    void queryClient.invalidateQueries({ queryKey: ['group-account-search', tenantId] })
  }
  const link = useMutation({
    mutationFn: (accountId: string) => groupsAPI.linkAccount(group.id, accountId),
    onSuccess: () => {
      invalidate()
      setEditing(false)
      setQuery('')
    },
    onError: (err) => setError(groupErrorMessage(err)),
  })
  const unlink = useMutation({
    mutationFn: () => groupsAPI.unlinkAccount(group.id),
    onSuccess: invalidate,
    onError: (err) => setError(groupErrorMessage(err)),
  })

  if (!editing) {
    return (
      <div className="flex items-center gap-1.5">
        {group.account_name ? (
          <Badge variant="info" size="sm">
            {group.account_name}
          </Badge>
        ) : (
          <span className="text-xs text-text-tertiary">Sem cliente vinculado</span>
        )}
        {canManage && (
          <button type="button" onClick={() => setEditing(true)} className="text-xs font-medium text-accent-primary hover:underline">
            {group.account_name ? 'Alterar' : 'Vincular'}
          </button>
        )}
        {canManage && group.account_name && (
          <button type="button" disabled={unlink.isPending} onClick={() => unlink.mutate()} className="text-xs text-text-tertiary hover:underline">
            Remover
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
        <Button
          size="sm"
          variant="tertiary"
          onClick={() => {
            setEditing(false)
            setQuery('')
          }}
        >
          Cancelar
        </Button>
      </div>
      <ul className="max-h-40 overflow-y-auto rounded-control border border-border-subtle bg-surface">
        {search.isLoading ? (
          <li className="px-2 py-1.5 text-xs text-text-tertiary">Carregando...</li>
        ) : (search.data ?? []).length === 0 ? (
          <li className="px-2 py-1.5 text-xs text-text-tertiary">Nenhum cliente encontrado.</li>
        ) : (
          (search.data ?? []).map((a) => (
            <li key={a.id}>
              <button
                type="button"
                disabled={link.isPending}
                onClick={() => link.mutate(a.id)}
                className="block w-full truncate px-2 py-1.5 text-left text-xs text-text-primary hover:bg-surface-muted"
              >
                {a.name}
              </button>
            </li>
          ))
        )}
      </ul>
    </div>
  )
}
