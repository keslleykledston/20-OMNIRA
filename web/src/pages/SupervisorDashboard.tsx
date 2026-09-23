import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Card,
  EmptyState,
  ErrorState,
  PageHeader,
  Skeleton,
  StatusBadge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from '../components/primitives'
import { getTenantId } from '../lib/session'
import { agentsAPI, type OperationalAgent } from '../lib/agents'
import { presenceAPI } from '../lib/presence'
import { usePresenceEvents } from '../hooks/usePresenceEvents'
import { useAccess } from '../lib/useAccess'

// PRODUCT.1: real operational presence overview — same real sources already
// proven on /settings/agents (agentsAPI roster + presenceAPI snapshot + SSE
// transitions). Valkey remains the realtime presence source of truth
// (ADR-0010); Postgres last_seen_at is never consulted here. No queue load,
// ticket, SLA or account metrics — those are separate, not-yet-real domains
// (PRODUCT.0).
export default function SupervisorDashboardPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const canView = access.can('agent.read')

  const roster = useQuery({ queryKey: ['agents', tenantId], queryFn: agentsAPI.list, enabled: canView, retry: false })
  const presenceSnapshot = useQuery({ queryKey: ['agents-presence', tenantId], queryFn: presenceAPI.snapshot, enabled: canView, retry: false })

  const [onlineIds, setOnlineIds] = useState<Set<string>>(new Set())
  useEffect(() => {
    if (presenceSnapshot.data) setOnlineIds(new Set(presenceSnapshot.data.online_agent_profile_ids))
  }, [presenceSnapshot.data])
  usePresenceEvents({
    tenantId: tenantId ?? '',
    enabled: canView,
    onEvent: (event) => {
      setOnlineIds((current) => {
        const next = new Set(current)
        if (event.status === 'online') next.add(event.agent_profile_id)
        else next.delete(event.agent_profile_id)
        return next
      })
    },
  })

  const agents = roster.data ?? []
  const onlineCount = agents.filter((agent) => onlineIds.has(agent.id)).length
  const totalCount = agents.length
  const offlineCount = totalCount - onlineCount

  return (
    <div className="px-6 py-6 lg:px-8 lg:py-8">
      <PageHeader
        title="Supervisor"
        description="Presença em tempo real da equipe operacional."
        className="mb-6 border-b-0 bg-transparent p-0"
      />

      {!access.isLoading && !canView && (
        <ErrorState title="Sem permissão" message="Você não tem permissão para visualizar o supervisor." />
      )}

      {canView && (
        <div className="space-y-6">
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
            <Card padding="compact">
              <p className="text-sm text-text-secondary">Total de agentes</p>
              <p className="text-3xl font-bold text-text-primary mt-2">{totalCount}</p>
            </Card>
            <Card padding="compact">
              <p className="text-sm text-text-secondary">Online</p>
              <p className="text-3xl font-bold text-status-success mt-2">{onlineCount}</p>
            </Card>
            <Card padding="compact">
              <p className="text-sm text-text-secondary">Offline</p>
              <p className="text-3xl font-bold text-text-primary mt-2">{offlineCount}</p>
            </Card>
          </div>

          {roster.isLoading && <Skeleton className="h-48" />}
          {roster.isError && (
            <ErrorState
              title="Não foi possível carregar a equipe"
              message="Tente novamente."
              action={{ label: 'Tentar novamente', onClick: () => { void roster.refetch() } }}
            />
          )}
          {roster.data && roster.data.length === 0 && (
            <EmptyState title="Nenhum agente operacional" description="Perfis operacionais são ativados pela administração da equipe." />
          )}

          {roster.data && roster.data.length > 0 && (
            <>
              <div className="hidden md:block">
                <Table>
                  <TableHead>
                    <TableRow>
                      <TableHeaderCell>Nome</TableHeaderCell>
                      <TableHeaderCell>Função</TableHeaderCell>
                      <TableHeaderCell>Presença</TableHeaderCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {agents.map((agent) => (
                      <AgentPresenceRow key={agent.id} agent={agent} online={onlineIds.has(agent.id)} />
                    ))}
                  </TableBody>
                </Table>
              </div>

              <div className="md:hidden space-y-3">
                {agents.map((agent) => (
                  <AgentPresenceCard key={agent.id} agent={agent} online={onlineIds.has(agent.id)} />
                ))}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}

function AgentPresenceRow({ agent, online }: { agent: OperationalAgent; online: boolean }) {
  return (
    <TableRow>
      <TableCell>
        <div>{agent.name || agent.email}</div>
        <div className="text-text-secondary">{agent.email}</div>
      </TableCell>
      <TableCell className="text-text-secondary">{agent.role}</TableCell>
      <TableCell>
        <StatusBadge status={online ? 'success' : 'default'}>{online ? 'Online' : 'Offline'}</StatusBadge>
      </TableCell>
    </TableRow>
  )
}

function AgentPresenceCard({ agent, online }: { agent: OperationalAgent; online: boolean }) {
  return (
    <div className="rounded-card border border-border-subtle bg-surface p-4">
      <div className="min-w-0">
        <p className="font-medium text-text-primary truncate">{agent.name || agent.email}</p>
        <p className="text-sm text-text-secondary truncate">{agent.email}</p>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <span className="text-sm text-text-secondary">{agent.role}</span>
        <StatusBadge status={online ? 'success' : 'default'}>{online ? 'Online' : 'Offline'}</StatusBadge>
      </div>
    </div>
  )
}
