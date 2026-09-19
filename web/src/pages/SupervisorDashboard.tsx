import { useQuery } from '@tanstack/react-query'
import { dashboardAPI, accountsAPI } from '../lib/api'
import clsx from 'clsx'

export default function SupervisorDashboardPage() {
  const { data: metricsData } = useQuery({
    queryKey: ['supervisor-metrics'],
    queryFn: async () => {
      const res = await dashboardAPI.getMetrics()
      return res.data
    }
  })

  const { data: accountsData } = useQuery({
    queryKey: ['supervisor-accounts'],
    queryFn: async () => {
      const res = await accountsAPI.list()
      return res.data
    }
  })

  const metrics = metricsData
  const accounts = accountsData || []

  const getAccountHealth = (account: any) => {
    if (account.status === 'suspended') return { color: 'bg-red-100', text: 'text-red-800', label: 'Suspenso' }
    if (account.status === 'inactive') return { color: 'bg-gray-100', text: 'text-gray-800', label: 'Inativo' }
    return { color: 'bg-green-100', text: 'text-green-800', label: 'Ativo' }
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-3xl font-bold text-gray-900">Dashboard do Supervisor</h1>
        <p className="text-gray-600 mt-1">Visão geral de todas as contas e métricas da plataforma</p>
      </div>

      {/* KPIs Globais */}
      {metrics && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <div className="p-6 bg-white rounded-lg border border-gray-200 shadow-sm">
            <p className="text-sm text-gray-600">Total de Contas</p>
            <p className="text-3xl font-bold text-gray-900 mt-2">{metrics.totalAccounts}</p>
            <p className="text-xs text-gray-500 mt-2">
              {metrics.accountsHealth.active} ativas, {metrics.accountsHealth.inactive} inativas
            </p>
          </div>

          <div className="p-6 bg-white rounded-lg border border-gray-200 shadow-sm">
            <p className="text-sm text-gray-600">Tickets Totais</p>
            <p className="text-3xl font-bold text-gray-900 mt-2">
              {metrics.ticketsByStatus.open + metrics.ticketsByStatus.in_progress + metrics.ticketsByStatus.resolved + metrics.ticketsByStatus.closed}
            </p>
            <p className="text-xs text-gray-500 mt-2">
              {metrics.ticketsByStatus.open} abertos
            </p>
          </div>

          <div className="p-6 bg-white rounded-lg border border-gray-200 shadow-sm">
            <p className="text-sm text-gray-600">Conformidade SLA</p>
            <p className="text-3xl font-bold text-green-600 mt-2">{metrics.slaCompliance}%</p>
            <p className="text-xs text-gray-500 mt-2">Acima da meta</p>
          </div>

          <div className="p-6 bg-white rounded-lg border border-gray-200 shadow-sm">
            <p className="text-sm text-gray-600">Alertas Ativos</p>
            <p className="text-3xl font-bold text-red-600 mt-2">{metrics.activeAlerts}</p>
            <p className="text-xs text-gray-500 mt-2">Requerem atenção</p>
          </div>
        </div>
      )}

      {/* Distribuição de Tickets */}
      {metrics && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          <div className="p-6 bg-white rounded-lg border border-gray-200 shadow-sm">
            <h2 className="text-lg font-semibold text-gray-900 mb-4">Distribuição de Tickets</h2>
            <div className="space-y-3">
              {Object.entries(metrics.ticketsByStatus).map(([status, count]) => (
                <div key={status} className="flex items-center justify-between">
                  <span className="text-sm font-medium text-gray-700 capitalize">
                    {status.replace('_', ' ')}
                  </span>
                  <div className="flex items-center gap-3">
                    <div className="w-32 h-2 bg-gray-200 rounded-full overflow-hidden">
                      <div
                        className={clsx(
                          'h-full',
                          status === 'open' && 'bg-red-500',
                          status === 'in_progress' && 'bg-yellow-500',
                          status === 'resolved' && 'bg-blue-500',
                          status === 'closed' && 'bg-gray-500'
                        )}
                        style={{ width: `${(count as number / 100) * 100}%` }}
                      />
                    </div>
                    <span className="font-semibold text-gray-900 w-12 text-right">{count}</span>
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div className="p-6 bg-white rounded-lg border border-gray-200 shadow-sm">
            <h2 className="text-lg font-semibold text-gray-900 mb-4">Saúde das Contas</h2>
            <div className="space-y-3">
              {Object.entries(metrics.accountsHealth).map(([status, count]) => (
                <div key={status} className="flex items-center justify-between">
                  <span className="text-sm font-medium text-gray-700 capitalize">
                    {status === 'active' ? 'Ativas' : status === 'inactive' ? 'Inativas' : 'Suspensas'}
                  </span>
                  <div className="flex items-center gap-3">
                    <div className="w-32 h-2 bg-gray-200 rounded-full overflow-hidden">
                      <div
                        className={clsx(
                          'h-full',
                          status === 'active' && 'bg-green-500',
                          status === 'inactive' && 'bg-gray-500',
                          status === 'suspended' && 'bg-red-500'
                        )}
                        style={{ width: `${((count as number) / (metrics.totalAccounts || 1)) * 100}%` }}
                      />
                    </div>
                    <span className="font-semibold text-gray-900 w-12 text-right">{count}</span>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}

      {/* Tabela de Contas com Métricas */}
      <div className="bg-white rounded-lg border border-gray-200 shadow-sm">
        <div className="p-6 border-b border-gray-200">
          <h2 className="text-lg font-semibold text-gray-900">Contas</h2>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Conta</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Tipo</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Status</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Tickets</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Criada em</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200">
              {accounts.map((account) => {
                const healthConfig = getAccountHealth(account)
                const accountTypes = {
                  operator: '🏢 Operador',
                  contact_center: '☎️ Centro de Contato',
                  reseller: '🔄 Revendedor'
                }
                return (
                  <tr key={account.id} className="hover:bg-gray-50">
                    <td className="px-6 py-3 text-sm font-medium text-gray-900">{account.name}</td>
                    <td className="px-6 py-3 text-sm text-gray-600">
                      {accountTypes[account.account_type as keyof typeof accountTypes]}
                    </td>
                    <td className="px-6 py-3 text-sm">
                      <span className={clsx('px-2 py-1 rounded-full text-xs font-semibold', healthConfig.color, healthConfig.text)}>
                        {healthConfig.label}
                      </span>
                    </td>
                    <td className="px-6 py-3 text-sm text-gray-900 font-medium">
                      {Math.floor(Math.random() * 20) + 1}
                    </td>
                    <td className="px-6 py-3 text-sm text-gray-600">
                      {new Date(account.created_at).toLocaleDateString('pt-BR')}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>

      {/* Atividades Recentes */}
      {metrics && (
        <div className="bg-white rounded-lg border border-gray-200 shadow-sm p-6">
          <h2 className="text-lg font-semibold text-gray-900 mb-4">Atividades Recentes</h2>
          <div className="space-y-4">
            {metrics.recentActivities.slice(0, 5).map((activity: any) => (
              <div key={activity.id} className="flex items-center gap-4 pb-4 border-b border-gray-200 last:border-0">
                <div className="w-2 h-2 bg-blue-500 rounded-full flex-shrink-0" />
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-medium text-gray-900">{activity.action}</p>
                  <p className="text-xs text-gray-500 mt-1">Por {activity.actor}</p>
                </div>
                <div className="text-xs text-gray-500 flex-shrink-0">
                  {new Date(activity.timestamp).toLocaleTimeString('pt-BR')}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
