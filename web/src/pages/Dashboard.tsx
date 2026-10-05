import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ErrorState, MetricCard, MetricCardSkeleton, PageHeader } from '../components/primitives'
import type { IconName, MetricTone } from '../components/primitives'
import { dashboardAPI, dashboardErrorMessage } from '../lib/dashboard'
import { presenceAPI } from '../lib/presence'
import { usePresenceEvents } from '../hooks/usePresenceEvents'
import { useAccess } from '../lib/useAccess'
import { getTenantId } from '../lib/session'
import { useAuthStore } from '../lib/store'

// PRODUCT.3-B / DESIGN.6-B: real operational snapshot only. Three durable
// Postgres counts (open conversations/tickets, total contacts) behind
// dashboard.read, plus agents online composed from the existing
// Valkey-backed presence snapshot (PRODUCT.1) — no second presence
// implementation, no polling loop of its own. No charts, no trends, no SLA,
// no fabricated activity feed: those require semantics/sources that do not
// exist yet (see PRODUCT.3 and DASHBOARD.0/DESIGN.6-A gates).
function firstName(full?: string) {
  return full?.trim().split(/\s+/)[0] ?? ''
}

interface StatCard {
  label: string
  value: number | undefined
  icon: IconName
  tone: MetricTone
  helperText: string
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
  // Tones follow the reading each metric deserves at a glance, not a fixed
  // palette rotation: open tickets draws attention (warning, never danger —
  // an open ticket queue is normal operation, not an incident on its own),
  // agents online is reassuring when non-zero (success), the rest are
  // neutral operational counts.
  const cards: StatCard[] = [
    { label: 'Conversas abertas', value: snapshot.data?.open_conversations, icon: 'conversations', tone: 'neutral', helperText: 'Em andamento agora' },
    { label: 'Tickets abertos', value: snapshot.data?.open_tickets, icon: 'tickets', tone: 'warning', helperText: 'Aguardando resolução' },
    { label: 'Contatos', value: snapshot.data?.total_contacts, icon: 'contacts', tone: 'info', helperText: 'Cadastrados no tenant' },
    { label: 'Não classificados', value: snapshot.data?.unclassified_contacts, icon: 'contacts', tone: 'warning', helperText: 'Contatos sem tipo (cliente ou outros)' },
    { label: 'Agentes online', value: canViewPresence ? onlineIds.size : undefined, icon: 'supervisor', tone: 'success', helperText: 'Disponíveis agora' },
  ]

  return (
    <div className="px-6 py-6 lg:px-8 lg:py-8">
      <PageHeader
        title="Visão geral"
        description={greeting ? `Olá, ${greeting} — resumo operacional do seu tenant agora.` : 'Resumo operacional do seu tenant agora.'}
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
        <section className="overflow-hidden rounded-card border border-border-subtle bg-surface shadow-sm">
          <div className="border-b border-border-subtle px-5 py-4 sm:px-6">
            <h2 className="text-body-md font-bold text-text-primary">Indicadores operacionais</h2>
            <p className="mt-0.5 text-xs text-text-tertiary">Atualizado agora</p>
          </div>
          <div className="grid grid-cols-1 gap-4 p-5 sm:grid-cols-2 sm:p-6 lg:grid-cols-3 xl:grid-cols-5">
            {snapshot.isLoading
              ? Array.from({ length: 5 }).map((_, i) => <MetricCardSkeleton key={i} />)
              : cards.map((c) => (
                  <MetricCard key={c.label} label={c.label} value={c.value} icon={c.icon} tone={c.tone} helperText={c.helperText} />
                ))}
          </div>
        </section>
      )}
    </div>
  )
}
