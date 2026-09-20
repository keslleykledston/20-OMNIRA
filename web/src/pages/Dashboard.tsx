import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { useAuthStore } from '../lib/store'
import { EmptyState, ErrorState, Skeleton } from '../components/primitives'
import { PageHeader } from '../components/primitives/PageHeader'
import { dashboardRepository } from '../features/dashboard/data/repository'
import type { PeriodId } from '../features/dashboard/types'
import { MetricCard, MetricCardSkeleton } from '../features/dashboard/components/MetricCard'
import { PeriodSelector } from '../features/dashboard/components/PeriodSelector'
import { SectionCard } from '../features/dashboard/components/SectionCard'
import { ChannelBarChart, ChannelLegend } from '../features/dashboard/components/ChannelBarChart'
import { TicketStatusDonut, TicketStatusLegend } from '../features/dashboard/components/TicketStatusDonut'
import { ConversationRow, PriorityTicketRow } from '../features/dashboard/components/ActivityRows'

const CHART_HEIGHT = 'h-[200px]'

function firstName(full?: string) {
  return full?.trim().split(/\s+/)[0] ?? ''
}

export default function Dashboard() {
  const navigate = useNavigate()
  const { user } = useAuthStore()
  const [period, setPeriod] = useState<PeriodId>('last_24h')

  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ['dashboard', period],
    queryFn: () => dashboardRepository.getDashboard(period),
    staleTime: 30_000,
  })

  const dateLabel = new Date().toLocaleDateString('pt-BR', {
    day: '2-digit',
    month: 'short',
    year: 'numeric',
  })

  const greeting = firstName(user?.name)

  return (
    <div className="px-6 py-6 lg:px-8 lg:py-8">
      <PageHeader
        title={greeting ? `Olá, ${greeting} 👋` : 'Olá 👋'}
        description="Aqui está o resumo do seu atendimento hoje."
        className="mb-6 border-b-0 bg-transparent p-0"
        actions={<PeriodSelector value={period} onChange={setPeriod} dateLabel={dateLabel} />}
      />

      {isError ? (
        <ErrorState
          message="Não foi possível carregar os indicadores do período selecionado."
          action={{ label: 'Tentar novamente', onClick: () => void refetch() }}
        />
      ) : (
        <div className="space-y-6">
          {/* KPIs */}
          <div className="grid grid-cols-1 gap-5 sm:grid-cols-2 xl:grid-cols-4">
            {isLoading
              ? Array.from({ length: 4 }).map((_, i) => <MetricCardSkeleton key={i} />)
              : data!.metrics.map((m) => <MetricCard key={m.id} metric={m} />)}
          </div>

          {/* Analytics */}
          <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
            <SectionCard title="Conversas por canal">
              {isLoading ? (
                <Skeleton width="w-full" height="h-[248px]" />
              ) : data!.channels.length === 0 ? (
                <EmptyState title="Sem conversas no período" description="Nenhum canal registrou conversas." />
              ) : (
                <>
                  <div className={CHART_HEIGHT}>
                    <ChannelBarChart series={data!.channels} />
                  </div>
                  <ChannelLegend series={data!.channels} />
                </>
              )}
            </SectionCard>

            <SectionCard title="Status dos tickets">
              {isLoading ? (
                <Skeleton width="w-full" height="h-[248px]" />
              ) : data!.ticketStatus.length === 0 ? (
                <EmptyState title="Sem tickets no período" description="Nada para exibir por aqui ainda." />
              ) : (
                <div className="flex flex-col items-center gap-6 sm:flex-row sm:gap-8">
                  <TicketStatusDonut slices={data!.ticketStatus} />
                  <TicketStatusLegend slices={data!.ticketStatus} />
                </div>
              )}
            </SectionCard>
          </div>

          {/* Activity */}
          <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
            <SectionCard
              title="Conversas recentes"
              action={{ label: 'Ver todas', onClick: () => navigate('/inbox') }}
            >
              {isLoading ? (
                <Skeleton width="w-full" height="h-14" count={4} />
              ) : data!.recentConversations.length === 0 ? (
                <EmptyState title="Nenhuma conversa recente" description="Novas conversas aparecem aqui." />
              ) : (
                <ul className="divide-y divide-border-subtle">
                  {data!.recentConversations.map((c) => (
                    <li key={c.id}>
                      <ConversationRow conversation={c} onOpen={() => navigate('/inbox')} />
                    </li>
                  ))}
                </ul>
              )}
            </SectionCard>

            <SectionCard
              title="Tickets prioritários"
              action={{ label: 'Ver todos', onClick: () => navigate('/tickets') }}
            >
              {isLoading ? (
                <Skeleton width="w-full" height="h-14" count={4} />
              ) : data!.priorityTickets.length === 0 ? (
                <EmptyState title="Nenhum ticket prioritário" description="Sua fila está em dia." />
              ) : (
                <ul className="divide-y divide-border-subtle">
                  {data!.priorityTickets.map((t) => (
                    <li key={t.id}>
                      <PriorityTicketRow ticket={t} onOpen={() => navigate('/tickets')} />
                    </li>
                  ))}
                </ul>
              )}
            </SectionCard>
          </div>
        </div>
      )}
    </div>
  )
}
