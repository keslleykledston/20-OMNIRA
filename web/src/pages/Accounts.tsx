import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { accountsAPI } from '../lib/api'

export default function Accounts() {
  const queryClient = useQueryClient()
  const [showForm, setShowForm] = useState(false)
  const [selectedAccount, setSelectedAccount] = useState<any>(null)
  const [formData, setFormData] = useState({ name: '', account_type: 'operator' })
  const [filter, setFilter] = useState('all')

  const { data: accounts = [], isLoading } = useQuery({
    queryKey: ['accounts'],
    queryFn: () => accountsAPI.list().then(r => r.data)
  })

  const createMutation = useMutation({
    mutationFn: (data: any) => accountsAPI.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['accounts'] })
      setShowForm(false)
      setFormData({ name: '', account_type: 'operator' })
    }
  })

  const suspendMutation = useMutation({
    mutationFn: (id: string) => accountsAPI.suspend(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['accounts'] })
    }
  })

  const reactivateMutation = useMutation({
    mutationFn: (id: string) => accountsAPI.reactivate(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['accounts'] })
    }
  })

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (formData.name.trim()) {
      createMutation.mutate(formData)
    }
  }

  const filteredAccounts = accounts.filter((acc: any) => {
    if (filter === 'all') return true
    return acc.status === filter
  })

  const accountTypeLabel = (type: string) => {
    const labels: Record<string, string> = {
      operator: 'Operador',
      contact_center: 'Central de Atendimento',
      reseller: 'Reseller'
    }
    return labels[type] || type
  }

  return (
    <div className="container py-8">
      <div className="flex justify-between items-center mb-8">
        <h1 className="text-3xl font-bold text-slate-900">Contas BPO</h1>
        <button
          onClick={() => setShowForm(!showForm)}
          className="btn-primary"
        >
          {showForm ? '✕ Cancelar' : '+ Nova Conta'}
        </button>
      </div>

      {showForm && (
        <div className="bg-white rounded-lg shadow p-6 mb-8 border-l-4 border-blue-500">
          <h2 className="text-lg font-semibold mb-4">Criar Nova Conta</h2>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label className="block text-sm font-medium text-slate-700 mb-1">
                Nome da Conta
              </label>
              <input
                type="text"
                placeholder="ex: Support Center SP"
                value={formData.name}
                onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                className="w-full px-4 py-2 border border-slate-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
            </div>

            <div>
              <label className="block text-sm font-medium text-slate-700 mb-1">
                Tipo de Conta
              </label>
              <select
                value={formData.account_type}
                onChange={(e) => setFormData({ ...formData, account_type: e.target.value })}
                className="w-full px-4 py-2 border border-slate-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500"
              >
                <option value="operator">Operador</option>
                <option value="contact_center">Central de Atendimento</option>
                <option value="reseller">Reseller</option>
              </select>
            </div>

            <button
              type="submit"
              disabled={createMutation.isPending}
              className="btn-primary w-full disabled:opacity-50"
            >
              {createMutation.isPending ? 'Criando...' : 'Criar Conta'}
            </button>
          </form>
        </div>
      )}

      {/* Filtros */}
      <div className="mb-6 flex gap-2">
        <button
          onClick={() => setFilter('all')}
          className={`px-4 py-2 rounded-lg text-sm font-medium transition-colors ${
            filter === 'all'
              ? 'bg-blue-600 text-white'
              : 'bg-slate-200 text-slate-900 hover:bg-slate-300'
          }`}
        >
          Todas
        </button>
        <button
          onClick={() => setFilter('active')}
          className={`px-4 py-2 rounded-lg text-sm font-medium transition-colors ${
            filter === 'active'
              ? 'bg-green-600 text-white'
              : 'bg-slate-200 text-slate-900 hover:bg-slate-300'
          }`}
        >
          Ativas ({accounts.filter((a: any) => a.status === 'active').length})
        </button>
        <button
          onClick={() => setFilter('inactive')}
          className={`px-4 py-2 rounded-lg text-sm font-medium transition-colors ${
            filter === 'inactive'
              ? 'bg-yellow-600 text-white'
              : 'bg-slate-200 text-slate-900 hover:bg-slate-300'
          }`}
        >
          Inativas ({accounts.filter((a: any) => a.status === 'inactive').length})
        </button>
      </div>

      {/* Tabela */}
      {isLoading ? (
        <div className="text-center py-8 text-slate-500">Carregando contas...</div>
      ) : (
        <div className="bg-white rounded-lg shadow overflow-hidden">
          <table className="w-full">
            <thead className="bg-slate-50 border-b border-slate-200">
              <tr>
                <th className="px-6 py-3 text-left text-sm font-semibold text-slate-700">Nome</th>
                <th className="px-6 py-3 text-left text-sm font-semibold text-slate-700">Tipo</th>
                <th className="px-6 py-3 text-left text-sm font-semibold text-slate-700">Status</th>
                <th className="px-6 py-3 text-left text-sm font-semibold text-slate-700">Criada em</th>
                <th className="px-6 py-3 text-left text-sm font-semibold text-slate-700">Ações</th>
              </tr>
            </thead>
            <tbody>
              {filteredAccounts.length === 0 ? (
                <tr>
                  <td colSpan={5} className="px-6 py-8 text-center text-slate-500">
                    Nenhuma conta encontrada
                  </td>
                </tr>
              ) : (
                filteredAccounts.map((account: any) => (
                  <tr key={account.id} className="border-b border-slate-200 hover:bg-slate-50">
                    <td className="px-6 py-4 text-sm font-medium text-slate-900">
                      {account.name}
                    </td>
                    <td className="px-6 py-4 text-sm text-slate-600">
                      {accountTypeLabel(account.account_type)}
                    </td>
                    <td className="px-6 py-4 text-sm">
                      <span
                        className={`badge ${
                          account.status === 'active'
                            ? 'badge-success'
                            : account.status === 'suspended'
                            ? 'badge-error'
                            : 'badge-warning'
                        }`}
                      >
                        {account.status === 'active' ? '✓ Ativa' :
                         account.status === 'suspended' ? '⊗ Suspensa' : '○ Inativa'}
                      </span>
                    </td>
                    <td className="px-6 py-4 text-sm text-slate-600">
                      {new Date(account.created_at).toLocaleDateString('pt-BR')}
                    </td>
                    <td className="px-6 py-4 text-sm space-x-2">
                      <button
                        onClick={() => setSelectedAccount(account)}
                        className="text-blue-600 hover:text-blue-800 font-medium"
                      >
                        Ver
                      </button>
                      {account.status === 'active' ? (
                        <button
                          onClick={() => suspendMutation.mutate(account.id)}
                          disabled={suspendMutation.isPending}
                          className="text-red-600 hover:text-red-800 font-medium disabled:opacity-50"
                        >
                          Suspender
                        </button>
                      ) : (
                        <button
                          onClick={() => reactivateMutation.mutate(account.id)}
                          disabled={reactivateMutation.isPending}
                          className="text-green-600 hover:text-green-800 font-medium disabled:opacity-50"
                        >
                          Ativar
                        </button>
                      )}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      )}

      {/* Modal de Detalhes */}
      {selectedAccount && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
          <div className="bg-white rounded-lg shadow-lg p-6 max-w-md w-full">
            <div className="flex justify-between items-start mb-4">
              <h2 className="text-lg font-semibold text-slate-900">{selectedAccount.name}</h2>
              <button
                onClick={() => setSelectedAccount(null)}
                className="text-slate-400 hover:text-slate-600"
              >
                ✕
              </button>
            </div>

            <div className="space-y-4">
              <div className="bg-slate-50 p-4 rounded-lg">
                <div className="text-xs font-medium text-slate-500 uppercase mb-2">Tipo</div>
                <div className="text-sm font-semibold text-slate-900">
                  {accountTypeLabel(selectedAccount.account_type)}
                </div>
              </div>

              <div className="bg-slate-50 p-4 rounded-lg">
                <div className="text-xs font-medium text-slate-500 uppercase mb-2">Status</div>
                <div className="text-sm font-semibold">
                  <span
                    className={`badge ${
                      selectedAccount.status === 'active'
                        ? 'badge-success'
                        : 'badge-warning'
                    }`}
                  >
                    {selectedAccount.status}
                  </span>
                </div>
              </div>

              <div className="bg-slate-50 p-4 rounded-lg">
                <div className="text-xs font-medium text-slate-500 uppercase mb-2">ID</div>
                <div className="text-xs font-mono text-slate-600 break-all">
                  {selectedAccount.id}
                </div>
              </div>

              <div className="border-t border-slate-200 pt-4">
                <button
                  onClick={() => setSelectedAccount(null)}
                  className="btn-secondary w-full"
                >
                  Fechar
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
