import { useAuthStore } from '../lib/store'
import { useQuery } from '@tanstack/react-query'
import { dashboardAPI } from '../lib/api'

export default function Dashboard() {
  const { user } = useAuthStore()
  const { data: metrics } = useQuery({
    queryKey: ['dashboard-metrics'],
    queryFn: () => dashboardAPI.getMetrics().then(r => r.data),
    staleTime: 30000
  })

  const formatTime = (date: Date) => {
    const now = new Date()
    const diff = Math.floor((now.getTime() - date.getTime()) / 1000)

    if (diff < 60) return 'Agora'
    if (diff < 3600) return `${Math.floor(diff / 60)}m atrás`
    if (diff < 86400) return `${Math.floor(diff / 3600)}h atrás`
    return `${Math.floor(diff / 86400)}d atrás`
  }

  return (
    <div className="container py-8">
      {/* KPI Cards */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
        <div className="bg-white rounded-lg shadow p-6 border-l-4 border-blue-500">
          <div className="text-slate-600 text-sm font-medium">Total de Contas</div>
          <div className="text-3xl font-bold text-slate-900 mt-2">{metrics?.totalAccounts || 0}</div>
          <div className="text-xs text-green-600 mt-2">
            {metrics?.accountsHealth?.active || 0} ativas
          </div>
        </div>

        <div className="bg-white rounded-lg shadow p-6 border-l-4 border-green-500">
          <div className="text-slate-600 text-sm font-medium">Tickets Abertos</div>
          <div className="text-3xl font-bold text-slate-900 mt-2">{metrics?.openTickets || 0}</div>
          <div className="text-xs text-slate-500 mt-2">
            {metrics?.ticketsByStatus?.in_progress || 0} em progresso
          </div>
        </div>

        <div className="bg-white rounded-lg shadow p-6 border-l-4 border-yellow-500">
          <div className="text-slate-600 text-sm font-medium">Conformidade SLA</div>
          <div className="text-3xl font-bold text-slate-900 mt-2">{metrics?.slaCompliance || 0}%</div>
          <div className="text-xs text-slate-500 mt-2">Meta: 95%</div>
        </div>

        <div className="bg-white rounded-lg shadow p-6 border-l-4 border-red-500">
          <div className="text-slate-600 text-sm font-medium">Alertas Ativos</div>
          <div className="text-3xl font-bold text-slate-900 mt-2">{metrics?.activeAlerts || 0}</div>
          <div className={`text-xs mt-2 ${(metrics?.activeAlerts || 0) > 0 ? 'text-red-600' : 'text-green-600'}`}>
            {(metrics?.activeAlerts || 0) > 0 ? 'Requer atenção' : 'Sem alertas'}
          </div>
        </div>
      </div>

      {/* Main Content */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Atividade Recente */}
        <div className="lg:col-span-2 bg-white rounded-lg shadow p-6">
          <h2 className="text-lg font-semibold text-slate-900 mb-4">Atividade Recente</h2>
          <div className="space-y-4">
            {(metrics?.recentActivities || []).slice(0, 5).map((activity: any) => (
              <div key={activity.id} className="flex items-center gap-4 pb-4 border-b border-slate-200 last:border-0 last:pb-0">
                <div className="w-10 h-10 bg-gradient-to-br from-blue-400 to-blue-600 rounded-full flex items-center justify-center text-white text-sm font-semibold">
                  {activity.actor.charAt(0).toUpperCase()}
                </div>
                <div className="flex-1">
                  <div className="text-sm font-medium text-slate-900">{activity.action}</div>
                  <div className="text-xs text-slate-500">{formatTime(activity.timestamp)}</div>
                </div>
                <div className="text-xs text-slate-400">{activity.actor}</div>
              </div>
            ))}
          </div>
        </div>

        {/* Resumo do Usuário */}
        <div className="bg-white rounded-lg shadow p-6">
          <h2 className="text-lg font-semibold text-slate-900 mb-4">Bem-vindo!</h2>

          {/* User Info */}
          <div className="mb-6 pb-6 border-b border-slate-200">
            <div className="flex items-center gap-3 mb-4">
              <div className="w-12 h-12 bg-gradient-to-br from-blue-400 to-blue-600 rounded-full flex items-center justify-center text-white font-semibold">
                {user?.name?.charAt(0).toUpperCase()}
              </div>
              <div>
                <div className="font-semibold text-slate-900">{user?.name}</div>
                <div className="text-xs text-slate-500">{user?.email}</div>
              </div>
            </div>
            <div className="text-sm text-slate-600">
              <span className="inline-block bg-blue-100 text-blue-800 px-2 py-1 rounded text-xs font-medium">
                {user?.roles?.[0] || 'User'}
              </span>
            </div>
          </div>

          {/* Status Resumido */}
          <div className="space-y-3">
            <div>
              <div className="text-xs font-medium text-slate-500 uppercase">Contas Ativas</div>
              <div className="text-2xl font-bold text-slate-900">{metrics?.accountsHealth?.active || 0}</div>
            </div>
            <div>
              <div className="text-xs font-medium text-slate-500 uppercase">Tickets Pendentes</div>
              <div className="text-2xl font-bold text-slate-900">
                {(metrics?.ticketsByStatus?.open || 0) + (metrics?.ticketsByStatus?.in_progress || 0)}
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* Distribuição de Tickets */}
      {metrics?.ticketsByStatus && (
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-6 mt-8">
          <div className="bg-white rounded-lg shadow p-6">
            <h3 className="text-sm font-semibold text-slate-900 mb-4">Distribuição de Tickets</h3>
            <div className="space-y-3">
              <div>
                <div className="flex justify-between text-sm mb-1">
                  <span className="text-slate-600">Abertos</span>
                  <span className="font-semibold">{metrics.ticketsByStatus.open}</span>
                </div>
                <div className="w-full bg-slate-200 rounded-full h-2">
                  <div className="bg-blue-500 h-2 rounded-full" style={{width: `${Math.min((metrics.ticketsByStatus.open / (metrics.openTickets || 1)) * 100, 100)}%`}}></div>
                </div>
              </div>
              <div>
                <div className="flex justify-between text-sm mb-1">
                  <span className="text-slate-600">Em Progresso</span>
                  <span className="font-semibold">{metrics.ticketsByStatus.in_progress}</span>
                </div>
                <div className="w-full bg-slate-200 rounded-full h-2">
                  <div className="bg-yellow-500 h-2 rounded-full" style={{width: `${Math.min((metrics.ticketsByStatus.in_progress / (metrics.openTickets || 1)) * 100, 100)}%`}}></div>
                </div>
              </div>
              <div>
                <div className="flex justify-between text-sm mb-1">
                  <span className="text-slate-600">Resolvidos</span>
                  <span className="font-semibold">{metrics.ticketsByStatus.resolved}</span>
                </div>
                <div className="w-full bg-slate-200 rounded-full h-2">
                  <div className="bg-green-500 h-2 rounded-full" style={{width: `${metrics.ticketsByStatus.resolved > 0 ? 100 : 10}%`}}></div>
                </div>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
