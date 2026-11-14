import { useEffect, useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Modal, SearchField } from '../primitives'
import { useDebounced } from '../../hooks/useDebounced'
import { integrationsAPI } from '../../lib/integrations'
import { accountsAPI } from '../../lib/accounts'
import { accountDirectoryAPI } from '../../lib/contacts'
import { groupErrorMessage, groupsAPI, type AvailableGroup, type GroupAccountRef } from '../../lib/groups'
import { getTenantId } from '../../lib/session'

interface Props {
  open: boolean
  onClose: () => void
  onDone: () => void
}

type Option = { key: string; label: string; detail?: string; ref: GroupAccountRef }

// "Vincular Grupo": escolher um grupo do WhatsApp e um Cliente do CRM NUMA TACADA SÓ — hoje isso é duas
// ações em duas telas separadas (ManageGroupsModal para ativar, GroupAccountLink para vincular). Chama
// exatamente as mesmas duas rotas já existentes, em sequência (ativar, depois vincular); nenhum dado novo,
// nenhuma rota nova. Escolher um grupo já ativo pula direto para o passo do cliente (enable é idempotente).
export function LinkGroupModal({ open, onClose, onDone }: Props) {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const [step, setStep] = useState<'group' | 'client'>('group')
  const [search, setSearch] = useState('')
  const q = useDebounced(search.trim(), 300)
  const [connectionId, setConnectionId] = useState<string | undefined>(undefined)
  const [picked, setPicked] = useState<AvailableGroup | null>(null)
  const [clientQuery, setClientQuery] = useState('')
  const debouncedClientQuery = useDebounced(clientQuery, 300)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (open) {
      setStep('group')
      setSearch('')
      setConnectionId(undefined)
      setPicked(null)
      setClientQuery('')
      setError(null)
    }
  }, [open])

  const connections = useQuery({
    queryKey: ['groups-waha-connections', tenantId],
    queryFn: integrationsAPI.list,
    enabled: open && step === 'group',
    retry: false,
  })
  const instances = (connections.data ?? []).filter((c) => c.provider === 'waha' && c.status === 'active')
  useEffect(() => {
    if (instances.length === 1) setConnectionId(instances[0].id)
  }, [instances])
  const needsInstancePick = instances.length > 1 && !connectionId

  const available = useQuery({
    queryKey: ['groups-available', tenantId, q, connectionId],
    queryFn: () => groupsAPI.available(q, 50, connectionId),
    enabled: open && step === 'group' && !needsInstancePick,
    retry: false,
    placeholderData: keepPreviousData,
  })

  const clientSearch = useQuery({
    queryKey: ['group-account-search', tenantId, debouncedClientQuery],
    queryFn: () => accountsAPI.list({ q: debouncedClientQuery }),
    enabled: open && step === 'client',
  })
  const clientDirectory = useQuery({
    queryKey: ['group-account-directory', tenantId],
    queryFn: () => accountDirectoryAPI.directory(),
    enabled: open && step === 'client',
    retry: false,
  })
  const clientOptions = useMemo<Option[]>(() => {
    const dirItems = clientDirectory.data?.items ?? []
    const seen = new Set(dirItems.map((c) => c.name.trim().toLowerCase()))
    const needle = clientQuery.trim().toLowerCase()
    const dir = dirItems
      .filter((c) => !needle || c.name.toLowerCase().includes(needle) || (c.cnpj ?? '').includes(needle))
      .map((c): Option => ({ key: `d:${c.id}`, label: c.name, detail: c.cnpj ?? 'CRM', ref: { directoryCompanyId: c.id } }))
    const local = (clientSearch.data ?? [])
      .filter((a) => !seen.has(a.name.trim().toLowerCase()))
      .map((a): Option => ({ key: `a:${a.id}`, label: a.name, detail: 'Conta local', ref: { accountId: a.id } }))
    return [...dir, ...local]
  }, [clientDirectory.data, clientSearch.data, clientQuery])

  const confirm = useMutation({
    mutationFn: async (ref: GroupAccountRef) => {
      if (!picked) throw new Error('no group picked')
      const group = await groupsAPI.enable(picked.provider_group_id, connectionId)
      return groupsAPI.linkAccount(group.id, ref)
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['groups-list', tenantId] })
      void queryClient.invalidateQueries({ queryKey: ['groups-accounts-summary'] })
      onDone()
    },
    onError: (err) => setError(groupErrorMessage(err)),
  })

  const items = available.data?.items ?? []
  const total = available.data?.total ?? 0

  return (
    <Modal
      open={open}
      title="Vincular Grupo"
      description={
        step === 'group'
          ? 'Escolha o grupo do WhatsApp. Se ainda não estiver ativo, o OMNIRA passa a lê-lo agora.'
          : `Agora escolha o Cliente do CRM de "${picked?.name || picked?.provider_group_id}".`
      }
      onClose={onClose}
      footer={
        step === 'client' ? (
          <>
            <Button variant="secondary" size="sm" onClick={() => setStep('group')} disabled={confirm.isPending}>
              Voltar
            </Button>
            <Button variant="secondary" size="sm" onClick={onClose} disabled={confirm.isPending}>
              Cancelar
            </Button>
          </>
        ) : (
          <Button variant="secondary" size="sm" onClick={onClose}>
            Cancelar
          </Button>
        )
      }
    >
      {step === 'group' ? (
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
          <SearchField
            aria-label="Buscar grupo"
            placeholder="Buscar grupo pelo nome..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onClear={() => setSearch('')}
            disabled={needsInstancePick}
          />
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
                    {g.enabled && ' · já ativo'}
                  </p>
                </div>
                <Button size="sm" onClick={() => { setPicked(g); setStep('client') }}>
                  Escolher
                </Button>
              </li>
            ))}
          </ul>
          {total > items.length && (
            <p className="text-xs text-text-tertiary">
              Mostrando {items.length} de {total}. Refine a busca para achar outro grupo.
            </p>
          )}
        </div>
      ) : (
        <div className="space-y-3">
          <input
            autoFocus
            type="text"
            value={clientQuery}
            onChange={(e) => setClientQuery(e.target.value)}
            placeholder="Buscar cliente..."
            aria-label="Buscar cliente"
            className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary"
          />
          {clientDirectory.isError && (
            <p className="text-xs text-status-warning">Diretório de empresas do CRM indisponível: só contas já existentes aparecem.</p>
          )}
          <ul className="max-h-60 overflow-y-auto rounded-control border border-border-subtle bg-surface" role="listbox" aria-label="Clientes">
            {clientSearch.isLoading || clientDirectory.isLoading ? (
              <li className="px-2 py-1.5 text-xs text-text-tertiary">Carregando...</li>
            ) : clientOptions.length === 0 ? (
              <li className="px-2 py-1.5 text-xs text-text-tertiary">Nenhum cliente encontrado.</li>
            ) : (
              clientOptions.map((o) => (
                <li key={o.key} role="option">
                  <button
                    type="button"
                    disabled={confirm.isPending}
                    onClick={() => confirm.mutate(o.ref)}
                    className="flex w-full items-center justify-between gap-2 truncate px-2 py-1.5 text-left text-sm text-text-primary hover:bg-surface-muted"
                  >
                    <span className="truncate">{o.label}</span>
                    {o.detail && <span className="shrink-0 text-xs text-text-tertiary">{o.detail}</span>}
                  </button>
                </li>
              ))
            )}
          </ul>
          {error && (
            <p role="alert" className="text-sm text-status-danger">
              {error}
            </p>
          )}
        </div>
      )}
    </Modal>
  )
}
