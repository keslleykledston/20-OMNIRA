import axios from 'axios'
import { endSessionUrlFrom } from './logout'

const api = axios.create({
  baseURL: '/api',
  withCredentials: true,
  headers: {
    'Content-Type': 'application/json'
  }
})

api.interceptors.request.use(config => {
  const token = localStorage.getItem('token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

api.interceptors.response.use(
  response => response,
  error => {
    if (error.response?.status === 401 && !window.location.pathname.includes('/login')) {
      localStorage.removeItem('token')
      localStorage.removeItem('user')
      // Usar replace para evitar histórico
      window.location.replace('/login')
    }
    return Promise.reject(error)
  }
)

// Não existe autenticação local por senha neste produto: produção entra por
// OIDC/SSO. Por isso não há `login(email, password)` — assinatura que existia
// antes e cujo segundo parâmetro nunca era enviado a lugar nenhum.
export interface AuthMode {
  mode: 'oidc' | 'dev' | 'unavailable'
  dev_auth?: boolean
}

export const authAPI = {
  // O servidor é quem diz o que existe; a tela não deve oferecer outro caminho.
  mode: () => api.get<AuthMode>('/v1/auth/mode'),
  // returnTo é validado de novo no backend contra uma allowlist (só
  // /invite/:token hoje); um valor fora dela é simplesmente ignorado lá,
  // nunca vira redirect aberto.
  startOIDC: (returnTo?: string) => {
    const url = returnTo ? `/api/v1/auth/oidc/start?return_to=${encodeURIComponent(returnTo)}` : '/api/v1/auth/oidc/start'
    window.location.assign(url)
  },
  session: () => api.get('/v1/auth/session'),
  // Ferramenta de desenvolvimento. A rota só existe quando o servidor a
  // registra (ambiente permitido + OMNIRA_DEV_AUTH_ENABLED), e responde 404
  // caso contrário. Sem senha, porque não há senha a verificar.
  devLogin: async (email: string) => {
    const res = await api.post('/v1/auth/dev/login', { email })
    const { token, user, tenant } = res.data
    return { data: { token, user: { ...user, roles: user?.roles ?? [] }, tenant } }
  },
  logout: async () => {
    const res = await api.post('/v1/auth/logout').catch(() => undefined)
    localStorage.removeItem('token')
    localStorage.removeItem('user')
    // When set, the caller must send the browser there so the identity provider's
    // own session ends too (otherwise the next login reuses the previous account).
    return { data: { status: 'ok', endSessionUrl: endSessionUrlFrom(res?.data) } }
  },
  refresh: () => api.post('/v1/auth/refresh')
}

const MOCK_TEMPLATES = [
  {
    id: 'sla-compliance',
    name: 'Conformidade SLA',
    description: 'Relatório mensal de cumprimento de SLA por conta',
    icon: '📊'
  },
  {
    id: 'ticket-metrics',
    name: 'Métricas de Tickets',
    description: 'Volume, tempo médio de resolução e distribuição por prioridade',
    icon: '📈'
  },
  {
    id: 'team-performance',
    name: 'Performance da Equipe',
    description: 'Análise de produtividade por atendente',
    icon: '👥'
  },
  {
    id: 'account-health',
    name: 'Saúde das Contas',
    description: 'Status e métricas de cada conta BPO',
    icon: '🏥'
  }
]

export const reportsAPI = {
  list: () =>
    Promise.resolve({
      data: MOCK_TEMPLATES
    }),

  getTemplate: (id: string) =>
    Promise.resolve({
      data: MOCK_TEMPLATES.find(t => t.id === id) || {
        id,
        name: 'Template Padrão',
        description: 'Relatório padrão',
        icon: '📄'
      }
    }),

  generate: (id: string, params?: any) =>
    Promise.resolve({
      data: {
        id: `RPT-${Date.now()}`,
        template_id: id,
        status: 'completed',
        generated_at: new Date().toISOString(),
        data: {
          title: MOCK_TEMPLATES.find(t => t.id === id)?.name || 'Relatório',
          period: params?.period || 'Setembro 2026',
          summary: {
            totalTickets: 156,
            resolvedTickets: 142,
            averageResolutionTime: '18.5 horas',
            slaCompliance: '94.5%',
            totalAccounts: 4
          },
          metrics: [
            { label: 'Tickets Abertos', value: 14, trend: '+2%' },
            { label: 'Tickets em Progresso', value: 3, trend: '-1%' },
            { label: 'Conformidade SLA', value: '94.5%', trend: '+0.5%' },
            { label: 'Tempo Médio', value: '18.5h', trend: '-2.3%' }
          ]
        }
      }
    }),

  exportJSON: (id: string) =>
    Promise.resolve({
      data: JSON.stringify({
        template_id: id,
        generated_at: new Date().toISOString(),
        content: MOCK_TEMPLATES.find(t => t.id === id) || {}
      }, null, 2)
    }),

  exportCSV: (id: string) =>
    Promise.resolve({
      data: `Template,${id}\nGerado em,${new Date().toISOString()}\nStatus,Completo`
    })
}

export default api
