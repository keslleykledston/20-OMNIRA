import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  Input,
  Modal,
  Skeleton,
  StatusBadge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from '../components/primitives'
import { SettingsShell, SETTINGS_SECTIONS } from '../components/SettingsShell'
import { getTenantId } from '../lib/session'
import { agentsAPI, type OperationalAgent } from '../lib/agents'
import { presenceAPI } from '../lib/presence'
import { usePresenceEvents } from '../hooks/usePresenceEvents'
import { useAccess } from '../lib/useAccess'

function queues(agent: OperationalAgent) {
  if (agent.queues.length === 0) return 'Sem filas'
  return agent.queues.map((queue) => `${queue.queue_name} · ${queue.available ? 'Elegível' : 'Indisponível'} · cap. ${queue.capacity}`).join(' | ')
}

export default function AgentsPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const queryClient = useQueryClient()
  const canManage = access.can('agent.manage')
  const [selected, setSelected] = useState<OperationalAgent | null>(null)
  const [queueID, setQueueID] = useState('')
  const [capacity, setCapacity] = useState('1')
  const [available, setAvailable] = useState(true)
  const [eligibilityDraft, setEligibilityDraft] = useState<Record<string, boolean>>({})
  const result = useQuery({ queryKey: ['agents', tenantId], queryFn: agentsAPI.list, enabled: access.can('agent.read'), retry: false })
  // ADR-0010 §11-12: online/offline only, read-only, never gates routing or
  // agent management here. Snapshot is the initial GET; SSE carries only
  // aggregated transitions afterwards.
  const presenceSnapshot = useQuery({ queryKey: ['agents-presence', tenantId], queryFn: presenceAPI.snapshot, enabled: access.can('agent.read'), retry: false })
  const [onlineIds, setOnlineIds] = useState<Set<string>>(new Set())
  useEffect(() => {
    if (presenceSnapshot.data) setOnlineIds(new Set(presenceSnapshot.data.online_agent_profile_ids))
  }, [presenceSnapshot.data])
  usePresenceEvents({
    tenantId: tenantId ?? '',
    enabled: access.can('agent.read'),
    onEvent: (event) => {
      setOnlineIds((current) => {
        const next = new Set(current)
        if (event.status === 'online') next.add(event.agent_profile_id)
        else next.delete(event.agent_profile_id)
        return next
      })
    },
  })
  const update = useMutation({
    mutationFn: ({ id, status }: { id: string; status: 'active' | 'disabled' }) => agentsAPI.setStatus(id, status),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['agents', tenantId] }),
  })
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['agents', tenantId] })
  const addQueue = useMutation({ mutationFn: () => agentsAPI.addQueue(selected!.id, queueID, available, Number(capacity)), onSuccess: () => { setQueueID(''); setCapacity('1'); setAvailable(true); refresh() } })
  const updateQueue = useMutation({
    mutationFn: ({ memberID, nextAvailable, nextCapacity }: { memberID: string; nextAvailable: boolean; nextCapacity: number }) => agentsAPI.updateQueue(selected!.id, memberID, nextAvailable, nextCapacity),
    onMutate: async ({ memberID, nextAvailable, nextCapacity }) => {
      await queryClient.cancelQueries({ queryKey: ['agents', tenantId] })
      const previous = queryClient.getQueryData<OperationalAgent[]>(['agents', tenantId])
      queryClient.setQueryData<OperationalAgent[]>(['agents', tenantId], (agents) => agents?.map((agent) => ({ ...agent, queues: agent.queues.map((queue) => queue.id === memberID ? { ...queue, available: nextAvailable, capacity: nextCapacity } : queue) })))
      return { previous }
    },
    onError: (_error, _variables, context) => queryClient.setQueryData(['agents', tenantId], context?.previous),
    onSettled: async () => {
      await refresh()
      setEligibilityDraft({})
    },
  })
  const removeQueue = useMutation({ mutationFn: (memberID: string) => agentsAPI.removeQueue(selected!.id, memberID), onSuccess: refresh })
  const current = selected && result.data?.find((agent) => agent.id === selected.id) || selected
  return (
    <SettingsShell
      sections={SETTINGS_SECTIONS}
      title="Agentes"
      description="Visão operacional de elegibilidade por fila. Isso não representa presença humana."
    >
      <div className="space-y-6">
        {!access.isLoading && !access.can('agent.read') && (
          <ErrorState title="Sem permissão" message="Você não tem permissão para visualizar agentes." />
        )}
        {result.isLoading && <Skeleton className="h-48" />}
        {result.isError && (
          <ErrorState
            title="Não foi possível carregar agentes"
            message="Tente novamente."
            action={{ label: 'Tentar novamente', onClick: () => { void result.refetch() } }}
          />
        )}
        {result.data && result.data.length === 0 && (
          <EmptyState title="Nenhum agente operacional" description="Perfis operacionais são ativados pela administração da equipe." />
        )}

        {result.data && result.data.length > 0 && (
          <>
            <div className="hidden md:block">
              <Table>
                <TableHead>
                  <TableRow>
                    <TableHeaderCell>Nome</TableHeaderCell>
                    <TableHeaderCell>Função</TableHeaderCell>
                    <TableHeaderCell>Estado operacional</TableHeaderCell>
                    <TableHeaderCell>Presença</TableHeaderCell>
                    <TableHeaderCell>Filas</TableHeaderCell>
                    <TableHeaderCell />
                  </TableRow>
                </TableHead>
                <TableBody>
                  {result.data.map((agent) => (
                    <AgentRow
                      key={agent.id}
                      agent={agent}
                      online={onlineIds.has(agent.id)}
                      canManage={canManage}
                      isTogglingStatus={update.isPending}
                      onOpenDetails={() => setSelected(agent)}
                      onToggleStatus={() => update.mutate({ id: agent.id, status: agent.status === 'active' ? 'disabled' : 'active' })}
                    />
                  ))}
                </TableBody>
              </Table>
            </div>

            <div className="md:hidden space-y-3">
              {result.data.map((agent) => (
                <AgentCard
                  key={agent.id}
                  agent={agent}
                  online={onlineIds.has(agent.id)}
                  canManage={canManage}
                  isTogglingStatus={update.isPending}
                  onOpenDetails={() => setSelected(agent)}
                  onToggleStatus={() => update.mutate({ id: agent.id, status: agent.status === 'active' ? 'disabled' : 'active' })}
                />
              ))}
            </div>
          </>
        )}
      </div>

      {current && (
        <Modal
          open
          title={`Filas de ${current.name || current.email}`}
          description="Elegibilidade e capacidade são locais à fila; não representam presença humana."
          onClose={() => { setSelected(null); addQueue.reset(); updateQueue.reset(); removeQueue.reset() }}
          footer={<Button variant="secondary" size="sm" onClick={() => setSelected(null)}>Fechar</Button>}
        >
          <div className="space-y-4">
            {current.queues.length === 0 && <p className="text-text-secondary">Nenhuma fila atribuída.</p>}
            {current.queues.map((queue) => (
              <div key={queue.id} className="rounded-control border border-border-subtle p-3">
                <div className="font-medium">{queue.queue_name}</div>
                <div className="mt-2 flex flex-wrap items-center gap-2">
                  <label className="flex items-center gap-2 text-sm">
                    <input
                      aria-label={`Elegibilidade ${queue.queue_name}`}
                      type="checkbox"
                      checked={eligibilityDraft[queue.id] ?? queue.available}
                      disabled={!canManage || updateQueue.isPending}
                      onChange={(event) => {
                        const nextAvailable = event.target.checked
                        setEligibilityDraft((draft) => ({ ...draft, [queue.id]: nextAvailable }))
                        updateQueue.mutate({ memberID: queue.id, nextAvailable, nextCapacity: queue.capacity })
                      }}
                    /> Elegível
                  </label>
                  <Input
                    aria-label={`Capacidade ${queue.queue_name}`}
                    type="number"
                    min="1"
                    defaultValue={queue.capacity}
                    className="w-24"
                    disabled={!canManage || updateQueue.isPending}
                    onBlur={(event) => {
                      const next = Number(event.target.value)
                      if (next >= 1 && next !== queue.capacity) updateQueue.mutate({ memberID: queue.id, nextAvailable: queue.available, nextCapacity: next })
                    }}
                  />
                  {canManage && (
                    <Button variant="danger" size="sm" isLoading={removeQueue.isPending} onClick={() => removeQueue.mutate(queue.id)}>Remover</Button>
                  )}
                </div>
              </div>
            ))}
            {canManage && (
              <div className="border-t border-border-subtle pt-4">
                <p className="mb-2 font-medium">Adicionar à fila</p>
                <div className="flex flex-wrap gap-2">
                  <Input label="ID da fila" value={queueID} onChange={(event) => setQueueID(event.target.value)} placeholder="UUID da fila" />
                  <Input label="Capacidade" type="number" min="1" value={capacity} onChange={(event) => setCapacity(event.target.value)} className="max-w-28" />
                  <label className="mt-8 flex items-center gap-2 text-sm">
                    <input aria-label="Elegível ao adicionar" type="checkbox" checked={available} onChange={(event) => setAvailable(event.target.checked)} /> Elegível
                  </label>
                  <Button className="mt-7" size="sm" isLoading={addQueue.isPending} disabled={!queueID || Number(capacity) < 1} onClick={() => addQueue.mutate()}>Adicionar fila</Button>
                </div>
              </div>
            )}
            {(addQueue.isError || updateQueue.isError || removeQueue.isError) && (
              <p role="alert" className="text-status-danger">Não foi possível salvar a associação. Tente novamente.</p>
            )}
            {(addQueue.isSuccess || updateQueue.isSuccess || removeQueue.isSuccess) && (
              <p role="status" className="text-status-success">Associação atualizada.</p>
            )}
          </div>
        </Modal>
      )}
    </SettingsShell>
  )
}

interface AgentRowProps {
  agent: OperationalAgent
  online: boolean
  canManage: boolean
  isTogglingStatus: boolean
  onOpenDetails: () => void
  onToggleStatus: () => void
}

function AgentRow({ agent, online, canManage, isTogglingStatus, onOpenDetails, onToggleStatus }: AgentRowProps) {
  return (
    <TableRow>
      <TableCell>
        <div>{agent.name || agent.email}</div>
        <div className="text-text-secondary">{agent.email}</div>
      </TableCell>
      <TableCell><Badge>{agent.role}</Badge></TableCell>
      <TableCell>
        <StatusBadge status={agent.status === 'active' ? 'success' : 'default'}>
          {agent.status === 'active' ? 'Ativo' : 'Desativado'}
        </StatusBadge>
      </TableCell>
      <TableCell>
        <StatusBadge status={online ? 'success' : 'default'}>{online ? 'Online' : 'Offline'}</StatusBadge>
      </TableCell>
      <TableCell className="text-text-secondary">{queues(agent)}</TableCell>
      <TableCell>
        <div className="flex gap-2">
          <Button variant="secondary" size="sm" onClick={onOpenDetails}>Detalhes</Button>
          {canManage && (
            <Button variant="secondary" size="sm" disabled={isTogglingStatus} onClick={onToggleStatus}>
              {agent.status === 'active' ? 'Desativar' : 'Ativar'}
            </Button>
          )}
        </div>
      </TableCell>
    </TableRow>
  )
}

function AgentCard({ agent, online, canManage, isTogglingStatus, onOpenDetails, onToggleStatus }: AgentRowProps) {
  return (
    <div className="rounded-card border border-border-subtle bg-surface p-4">
      <div className="min-w-0">
        <p className="font-medium text-text-primary truncate">{agent.name || agent.email}</p>
        <p className="text-sm text-text-secondary truncate">{agent.email}</p>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Badge>{agent.role}</Badge>
        <StatusBadge status={agent.status === 'active' ? 'success' : 'default'}>
          {agent.status === 'active' ? 'Ativo' : 'Desativado'}
        </StatusBadge>
        <StatusBadge status={online ? 'success' : 'default'}>{online ? 'Online' : 'Offline'}</StatusBadge>
      </div>
      <p className="mt-2 text-xs text-text-tertiary">{queues(agent)}</p>
      <div className="mt-3 flex gap-2">
        <Button variant="secondary" size="sm" onClick={onOpenDetails}>Detalhes</Button>
        {canManage && (
          <Button variant="secondary" size="sm" disabled={isTogglingStatus} onClick={onToggleStatus}>
            {agent.status === 'active' ? 'Desativar' : 'Ativar'}
          </Button>
        )}
      </div>
    </div>
  )
}
