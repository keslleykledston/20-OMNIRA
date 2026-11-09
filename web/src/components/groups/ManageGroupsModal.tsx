import { useEffect, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Modal, SearchField, StatusBadge } from '../primitives'
import { useDebounced } from '../../hooks/useDebounced'
import { integrationsAPI } from '../../lib/integrations'
import { groupErrorMessage, groupsAPI, type AvailableGroup } from '../../lib/groups'
import { getTenantId } from '../../lib/session'

interface Props {
  open: boolean
  onClose: () => void
}

// The administrator picks which of the account's WhatsApp groups OMNIRA reads. Nothing is read until a
// group is enabled here; disabling stops new messages but keeps the history (deleting is separate). A
// tenant may run more than one WhatsApp instance at once (two numbers connected side by side): their
// groups are never merged, so a connection must be picked before listing/enabling when there is more than one.
export function ManageGroupsModal({ open, onClose }: Props) {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const [search, setSearch] = useState('')
  const q = useDebounced(search.trim(), 300)
  const [error, setError] = useState<string | null>(null)
  const [connectionId, setConnectionId] = useState<string | undefined>(undefined)

  const connections = useQuery({
    queryKey: ['groups-waha-connections', tenantId],
    queryFn: integrationsAPI.list,
    enabled: open,
    retry: false,
  })
  const instances = (connections.data ?? []).filter((c) => c.provider === 'waha' && c.status === 'active')
  // One instance: behave exactly as before (no picker, no connection_id sent). More than one: a choice is
  // required before anything is listed, so the search below never has to guess which instance it searched.
  useEffect(() => {
    if (instances.length === 1) setConnectionId(instances[0].id)
  }, [instances])
  const needsInstancePick = instances.length > 1 && !connectionId

  const available = useQuery({
    queryKey: ['groups-available', tenantId, q, connectionId],
    queryFn: () => groupsAPI.available(q, 50, connectionId),
    enabled: open && !needsInstancePick,
    retry: false,
    // keep the list on screen while the next search loads, so typing does not blank it
    placeholderData: keepPreviousData,
  })

  const toggle = useMutation({
    mutationFn: (g: AvailableGroup) => (g.group_id ? groupsAPI.setEnabled(g.group_id, !g.enabled) : groupsAPI.enable(g.provider_group_id, connectionId)),
    onSuccess: () => {
      setError(null)
      void queryClient.invalidateQueries({ queryKey: ['groups-available', tenantId] })
      void queryClient.invalidateQueries({ queryKey: ['groups-list', tenantId] })
    },
    onError: (err) => setError(groupErrorMessage(err)),
  })

  const items = available.data?.items ?? []
  const total = available.data?.total ?? 0

  return (
    <Modal
      open={open}
      title="Gerenciar grupos"
      description="Escolha quais grupos do WhatsApp o OMNIRA lê. Grupos desligados não guardam nada."
      onClose={onClose}
      footer={
        <Button variant="secondary" size="sm" onClick={onClose}>
          Fechar
        </Button>
      }
    >
      <div className="space-y-3">
        {instances.length > 1 && (
          <label className="flex items-center gap-2 text-xs text-text-secondary">
            Instância do WhatsApp
            <select
              aria-label="Instância do WhatsApp"
              value={connectionId ?? ''}
              onChange={(e) => setConnectionId(e.target.value || undefined)}
              className="flex-1 rounded-control border border-border-subtle bg-surface px-2 py-1.5 text-sm text-text-primary focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary"
            >
              <option value="" disabled>
                Escolha uma instância...
              </option>
              {instances.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.external_account_id || c.id.slice(0, 8)}
                </option>
              ))}
            </select>
          </label>
        )}
        {needsInstancePick && (
          <p className="text-xs text-text-secondary">
            Esta empresa tem mais de uma instância do WhatsApp conectada. Escolha uma para ver seus grupos — os grupos de cada instância ficam separados.
          </p>
        )}

        <SearchField
          aria-label="Buscar grupo"
          placeholder="Buscar grupo pelo nome..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          onClear={() => setSearch('')}
          disabled={needsInstancePick}
        />

        {error && (
          <p role="alert" className="text-xs text-status-danger">
            {error}
          </p>
        )}
        {available.isError && (
          <p role="alert" className="text-sm text-status-danger">
            {groupErrorMessage(available.error)}
          </p>
        )}
        {available.isLoading && <p className="py-4 text-center text-sm text-text-secondary">Carregando grupos...</p>}
        {!available.isLoading && !available.isError && items.length === 0 && (
          <p className="py-4 text-center text-sm text-text-secondary">{q ? 'Nenhum grupo com esse nome.' : 'Nenhum grupo na conta do WhatsApp.'}</p>
        )}

        <ul className="max-h-80 divide-y divide-border-subtle overflow-y-auto rounded-control border border-border-subtle">
          {items.map((g) => (
            <li key={g.provider_group_id} className="flex items-center justify-between gap-3 px-3 py-2">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium text-text-primary">{g.name || 'Grupo sem nome'}</p>
                <p className="text-xs text-text-secondary">
                  {g.participant_count} {g.participant_count === 1 ? 'participante' : 'participantes'}
                </p>
              </div>
              <div className="flex flex-shrink-0 items-center gap-2">
                {g.enabled && <StatusBadge status="success" size="sm">Lendo</StatusBadge>}
                <Button
                  size="sm"
                  variant={g.enabled ? 'secondary' : 'primary'}
                  disabled={toggle.isPending}
                  onClick={() => toggle.mutate(g)}
                  aria-label={`${g.enabled ? 'Desativar' : 'Ativar'} ${g.name}`}
                >
                  {g.enabled ? 'Desativar' : 'Ativar'}
                </Button>
              </div>
            </li>
          ))}
        </ul>
        {total > items.length && (
          <p className="text-xs text-text-tertiary">
            Mostrando {items.length} de {total}. Refine a busca para achar outro grupo.
          </p>
        )}
      </div>
    </Modal>
  )
}
