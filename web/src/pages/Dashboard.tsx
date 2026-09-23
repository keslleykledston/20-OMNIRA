import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Card, ErrorState, Icon, PageHeader, Skeleton } from '../components/primitives'
import type { IconName } from '../components/primitives/Icon'
import { dashboardAPI, dashboardErrorMessage } from '../lib/dashboard'
import { presenceAPI } from '../lib/presence'
import { usePresenceEvents } from '../hooks/usePresenceEvents'
import { useAccess } from '../lib/useAccess'
import { getTenantId } from '../lib/session'
import { useAuthStore } from '../lib/store'

// PRODUCT.3-B: real operational snapshot only. Three durable Postgres counts
// (open conversations/tickets, total contacts) behind dashboard.read, plus
// agents online composed from the existing Valkey-backed presence snapshot
// (PRODUCT.1) — no second presence implementation, no polling loop of its
// own. No charts, no trends, no SLA, no fabricated activity feed: those
// require semantics/sources that do not exist yet (see PRODUCT.3 gate).
function firstName(full?: string) {
  return full?.trim().split(/\s+/)[0] ?? ''
}

interface StatCard {
  label: string
  value: number | undefined
  icon: IconName
}

export default function Dashboard() {
  const { user } = useAuthStore()
  const tenantId = getTenantId()
  const access = useAccess()
  const canViewDashboard = access.can('dashboard.read')
  const canViewPresence = access.can('agent.read')

  const snapshot = useQuery({
    queryKey: ['dashboard-snapshot', tenantId],
    queryFn: dashboardAPI.snapshot,
    enabled: canViewDashboard,
    retry: false,
  })

  const presenceSnapshot = useQuery({
    queryKey: ['agents-presence', tenantId],
    queryFn: presenceAPI.snapshot,
    enabled: canViewPresence,
    retry: false,
  })
  const [onlineIds, setOnlineIds] = useState<Set<string>>(new Set())
  useEffect(() => {
    if (presenceSnapshot.data) setOnlineIds(new Set(presenceSnapshot.data.online_agent_profile_ids))
  }, [presenceSnapshot.data])
  usePresenceEvents({
    tenantId: tenantId ?? '',
    enabled: canViewPresence,
    onEvent: (event) => {
      setOnlineIds((current) => {
        const next = new Set(current)
        if (event.status === 'online') next.add(event.agent_profile_id)
        else next.delete(event.agent_profile_id)
        return next
      })
    },
  })

  const greeting = firstName(user?.name)
  const cards: StatCard[] = [
    { label: 'Conversas abertas', value: snapshot.data?.open_conversations, icon: 'conversations' },
    { label: 'Tickets abertos', value: snapshot.data?.open_tickets, icon: 'tickets' },
    { label: 'Contatos', value: snapshot.data?.total_contacts, icon: 'contacts' },
    { label: 'Agentes online', value: canViewPresence ? onlineIds.size : undefined, icon: 'supervisor' },
  ]

  return (
    <div className="px-6 py-6 lg:px-8 lg:py-8">
      <PageHeader
        title={greeting ? `Olá, ${greeting} 👋` : 'Olá 👋'}
        description="Resumo operacional do seu tenant agora."
        className="mb-6 border-b-0 bg-transparent p-0"
      />

      {!access.isLoading && !canViewDashboard && (
        <ErrorState title="Sem permissão" message="Você não tem permissão para visualizar o Dashboard." />
      )}

      {canViewDashboard && snapshot.isError && (
        <ErrorState
          message={dashboardErrorMessage(snapshot.error)}
          action={{ label: 'Tentar novamente', onClick: () => void snapshot.refetch() }}
        />
      )}

      {canViewDashboard && !snapshot.isError && (
        <div className="grid grid-cols-1 gap-5 sm:grid-cols-2 xl:grid-cols-4">
          {snapshot.isLoading
            ? Array.from({ length: 4 }).map((_, i) => <StatCardSkeleton key={i} />)
            : cards.map((c) => <StatCardView key={c.label} card={c} />)}
        </div>
      )}
    </div>
  )
}

function StatCardView({ card }: { card: StatCard }) {
  return (
    <Card padding="compact" className="h-full">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="text-body-sm text-text-secondary truncate">{card.label}</p>
          <p className="mt-2 text-display-md font-bold text-text-primary">
            {card.value ?? '—'}
          </p>
        </div>
        <span className="flex-shrink-0 rounded-card p-2.5 bg-accent-primary-soft text-accent-primary">
          <Icon name={card.icon} size={22} />
        </span>
      </div>
    </Card>
  )
}

function StatCardSkeleton() {
  return (
    <Card padding="compact" className="h-full">
      <Skeleton width="w-24" height="h-4" />
      <div className="mt-3">
        <Skeleton width="w-16" height="h-8" />
      </div>
    </Card>
  )
}
