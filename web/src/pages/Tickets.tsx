import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ticketsAPI } from '../lib/api'
import type { Ticket } from '../lib/types'
import clsx from 'clsx'
import { isDevSurface, UnavailableSurface } from '../components/UnavailableSurface'

const statusConfig = {
  open: { color: 'bg-red-100', text: 'text-red-800', label: 'Aberto' },
  in_progress: { color: 'bg-yellow-100', text: 'text-yellow-800', label: 'Em Progresso' },
  resolved: { color: 'bg-blue-100', text: 'text-blue-800', label: 'Resolvido' },
  closed: { color: 'bg-gray-100', text: 'text-gray-800', label: 'Fechado' }
}

const priorityConfig = {
  low: { color: 'bg-green-100', text: 'text-green-800', label: 'Baixa' },
  medium: { color: 'bg-yellow-100', text: 'text-yellow-800', label: 'Média' },
  high: { color: 'bg-orange-100', text: 'text-orange-800', label: 'Alta' },
  critical: { color: 'bg-red-100', text: 'text-red-800', label: 'Crítica' }
}

const ASSIGNEES = [
  { id: 'test@omnira.local', name: 'Test User' },
  { id: 'admin@omnira.local', name: 'Admin User' }
]

export default function TicketsPage() {
  const queryClient = useQueryClient()
  const [filter, setFilter] = useState<string>('all')
  const [showForm, setShowForm] = useState(false)
  const [selectedTicket, setSelectedTicket] = useState<Ticket | null>(null)
  const [formData, setFormData] = useState({ title: '', priority: 'medium' as const })

  const { data: ticketsData, isLoading } = useQuery({
    queryKey: ['tickets', filter],
    queryFn: async () => {
      const res = await ticketsAPI.list(filter === 'all' ? undefined : { status: filter })
      return res.data
    }
  })

  const tickets = ticketsData || []

  const createMutation = useMutation({
    mutationFn: (data: any) => ticketsAPI.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets'] })
      setShowForm(false)
      setFormData({ title: '', priority: 'medium' })
    }
  })

  const assignMutation = useMutation({
    mutationFn: ({ id, userId }: { id: string; userId: string }) =>
      ticketsAPI.assign(id, userId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets'] })
      setSelectedTicket(null)
    }
  })

  const resolveMutation = useMutation({
    mutationFn: (id: string) => ticketsAPI.resolve(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets'] })
      setSelectedTicket(null)
    }
  })

  const closeMutation = useMutation({
    mutationFn: (id: string) => ticketsAPI.close(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tickets'] })
      setSelectedTicket(null)
    }
  })

  // ticketsAPI is a frontend fixture — the real backend only models a ticket
  // per conversation (see Inbox's TicketPanel), never a standalone list.
  // Never present the fixture as if it were real business data.
  if (!isDevSurface()) return <UnavailableSurface title="Tickets" />

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (formData.title.trim()) {
      createMutation.mutate(formData)
    }
  }

  const getStatusCounts = () => {
    const counts = { open: 0, in_progress: 0, resolved: 0, closed: 0 }
    tickets.forEach((t: any) => {
      if (t.status in counts) {
        counts[t.status as keyof typeof counts]++
      }
    })
    return counts
  }

  const counts = getStatusCounts()

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold text-gray-900">Tickets</h1>
          <p className="text-gray-600 mt-1">Gerenciamento de solicitações de suporte</p>
        </div>
        <button
          onClick={() => setShowForm(true)}
          className="btn-primary"
        >
          + Novo Ticket
        </button>
      </div>

      {/* Status Cards */}
      <div className="grid grid-cols-4 gap-4">
        {(
          [
            { key: 'open', label: 'Abertos', icon: '📋' },
            { key: 'in_progress', label: 'Em Progresso', icon: '⏳' },
            { key: 'resolved', label: 'Resolvidos', icon: '✅' },
            { key: 'closed', label: 'Fechados', icon: '🔒' }
          ] as const
        ).map(({ key, label, icon }) => (
          <div
            key={key}
            onClick={() => setFilter(filter === key ? 'all' : key)}
            className={clsx(
              'p-4 rounded-lg cursor-pointer transition',
              filter === key ? 'bg-blue-50 border-2 border-blue-500' : 'bg-white border border-gray-200 hover:border-gray-300'
            )}
          >
            <div className="text-2xl">{icon}</div>
            <div className="text-2xl font-bold text-gray-900 mt-2">{counts[key]}</div>
            <div className="text-sm text-gray-600">{label}</div>
          </div>
        ))}
      </div>

      {/* Tabela de Tickets */}
      <div className="bg-white rounded-lg border border-gray-200 shadow-sm">
        {isLoading ? (
          <div className="p-6 text-center text-gray-500">Carregando...</div>
        ) : tickets.length === 0 ? (
          <div className="p-6 text-center text-gray-500">Nenhum ticket encontrado</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead className="bg-gray-50 border-b border-gray-200">
                <tr>
                  <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">ID</th>
                  <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Título</th>
                  <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Prioridade</th>
                  <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Status</th>
                  <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Atribuído a</th>
                  <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Criado em</th>
                  <th className="px-6 py-3 text-left text-xs font-medium text-gray-700 uppercase">Ações</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-200">
                {tickets.map((ticket: any) => {
                  const priorityStyle = priorityConfig[ticket.priority as keyof typeof priorityConfig]
                  const statusStyle = statusConfig[ticket.status as keyof typeof statusConfig]
                  return (
                    <tr key={ticket.id} className="hover:bg-gray-50">
                      <td className="px-6 py-3 text-sm font-medium text-gray-900">{ticket.id}</td>
                      <td className="px-6 py-3 text-sm text-gray-600 max-w-xs truncate">{ticket.title}</td>
                      <td className="px-6 py-3 text-sm">
                        <span className={clsx('px-2 py-1 rounded-full text-xs font-semibold', priorityStyle?.color, priorityStyle?.text)}>
                          {priorityStyle?.label || ticket.priority}
                        </span>
                      </td>
                      <td className="px-6 py-3 text-sm">
                        <span className={clsx('px-2 py-1 rounded-full text-xs font-semibold', statusStyle?.color, statusStyle?.text)}>
                          {statusStyle?.label || ticket.status}
                        </span>
                      </td>
                      <td className="px-6 py-3 text-sm text-gray-600">
                        {ticket.assigned_to ? (
                          <span className="badge badge-success">{ticket.assigned_to.split('@')[0]}</span>
                        ) : (
                          <span className="text-gray-400">Não atribuído</span>
                        )}
                      </td>
                      <td className="px-6 py-3 text-sm text-gray-600">
                        {new Date(ticket.created_at).toLocaleDateString('pt-BR')}
                      </td>
                      <td className="px-6 py-3 text-sm">
                        <button
                          onClick={() => setSelectedTicket(ticket as Ticket)}
                          className="text-blue-600 hover:text-blue-900 font-medium"
                        >
                          Ver
                        </button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Modal: Novo Ticket */}
      {showForm && (
        <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
          <div className="bg-white rounded-lg p-6 max-w-md w-full mx-4">
            <h2 className="text-xl font-bold text-gray-900 mb-4">Criar Novo Ticket</h2>
            <form onSubmit={handleSubmit} className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-gray-700">Título</label>
                <input
                  type="text"
                  value={formData.title}
                  onChange={(e) => setFormData({ ...formData, title: e.target.value })}
                  placeholder="Descrição do problema"
                  className="w-full px-3 py-2 border border-gray-300 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700">Prioridade</label>
                <select
                  value={formData.priority}
                  onChange={(e) => setFormData({ ...formData, priority: e.target.value as any })}
                  className="w-full px-3 py-2 border border-gray-300 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                >
                  <option value="low">Baixa</option>
                  <option value="medium">Média</option>
                  <option value="high">Alta</option>
                  <option value="critical">Crítica</option>
                </select>
              </div>
              <div className="flex gap-3 justify-end">
                <button
                  type="button"
                  onClick={() => setShowForm(false)}
                  className="px-4 py-2 border border-gray-300 rounded-lg text-gray-700 hover:bg-gray-50"
                >
                  Cancelar
                </button>
                <button
                  type="submit"
                  disabled={createMutation.isPending}
                  className="btn-primary disabled:opacity-50"
                >
                  {createMutation.isPending ? 'Criando...' : 'Criar Ticket'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Modal: Detalhes do Ticket */}
      {selectedTicket && (
        <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
          <div className="bg-white rounded-lg p-6 max-w-md w-full mx-4">
            <h2 className="text-xl font-bold text-gray-900 mb-4">{selectedTicket.id}</h2>
            <div className="space-y-3 mb-4">
              <div>
                <label className="text-xs font-semibold text-gray-600 uppercase">Título</label>
                <p className="text-gray-900">{selectedTicket.title}</p>
              </div>
              <div>
                <label className="text-xs font-semibold text-gray-600 uppercase">Prioridade</label>
                <p className={clsx('inline-block px-2 py-1 rounded text-xs font-semibold', priorityConfig[selectedTicket.priority].color, priorityConfig[selectedTicket.priority].text)}>
                  {priorityConfig[selectedTicket.priority].label}
                </p>
              </div>
              <div>
                <label className="text-xs font-semibold text-gray-600 uppercase">Status</label>
                <p className={clsx('inline-block px-2 py-1 rounded text-xs font-semibold', statusConfig[selectedTicket.status].color, statusConfig[selectedTicket.status].text)}>
                  {statusConfig[selectedTicket.status].label}
                </p>
              </div>
              <div>
                <label className="text-xs font-semibold text-gray-600 uppercase">Atribuído a</label>
                {selectedTicket.assigned_to ? (
                  <p className="text-gray-900">{selectedTicket.assigned_to}</p>
                ) : (
                  <select
                    onChange={(e) => {
                      assignMutation.mutate({ id: selectedTicket.id, userId: e.target.value })
                    }}
                    className="w-full px-3 py-2 border border-gray-300 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                  >
                    <option value="">Selecione um atendente</option>
                    {ASSIGNEES.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}
                      </option>
                    ))}
                  </select>
                )}
              </div>
            </div>
            <div className="flex gap-2 justify-end">
              <button
                onClick={() => setSelectedTicket(null)}
                className="px-4 py-2 border border-gray-300 rounded-lg text-gray-700 hover:bg-gray-50"
              >
                Fechar
              </button>
              {(selectedTicket.status === 'open' || selectedTicket.status === 'in_progress') && (
                <button
                  onClick={() => resolveMutation.mutate(selectedTicket.id)}
                  disabled={resolveMutation.isPending}
                  className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 disabled:opacity-50"
                >
                  {resolveMutation.isPending ? 'Processando...' : 'Resolver'}
                </button>
              )}
              {selectedTicket.status === 'resolved' && (
                <button
                  onClick={() => closeMutation.mutate(selectedTicket.id)}
                  disabled={closeMutation.isPending}
                  className="px-4 py-2 bg-gray-600 text-white rounded-lg hover:bg-gray-700 disabled:opacity-50"
                >
                  {closeMutation.isPending ? 'Processando...' : 'Fechar'}
                </button>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
