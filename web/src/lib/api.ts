import axios from 'axios'

const api = axios.create({
  baseURL: '/api',
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

// Mock users para desenvolvimento
const MOCK_USERS: Record<string, any> = {
  'test@omnira.local': {
    id: '22222222-2222-2222-2222-222222222222',
    email: 'test@omnira.local',
    name: 'Test User',
    roles: ['admin']
  },
  'admin@omnira.local': {
    id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
    email: 'admin@omnira.local',
    name: 'Admin User',
    roles: ['admin']
  }
}

// Gerar JWT mock (para testes apenas)
const generateMockToken = (email: string) => {
  const header = btoa(JSON.stringify({ alg: 'HS256', typ: 'JWT' }))
  const now = Math.floor(Date.now() / 1000)
  const payload = btoa(JSON.stringify({
    sub: email,
    user_id: MOCK_USERS[email]?.id || 'user-' + Math.random(),
    tenant_id: '11111111-1111-1111-1111-111111111111',
    email,
    iat: now,
    exp: now + 86400,
    iss: 'omnira-mock',
    aud: 'omnira-api'
  }))
  const signature = btoa('mock-signature')
  return `${header}.${payload}.${signature}`
}

export const authAPI = {
  // Real backend login (POST /api/v1/auth/login). Returns the same shape the mock did.
  // NOTE: the backend endpoint is still a mock IdP (fixed known emails, per-process RS256 keys).
  login: async (email: string, _password: string) => {
    const res = await api.post('/v1/auth/login', { email })
    const { token, user, tenant } = res.data
    return { data: { token, user: { ...user, roles: user?.roles ?? [] }, tenant } }
  },
  // Offline dev/mock mode (VITE_MOCK_AUTH=true): fake unsigned token, NOT accepted by the backend.
  mockLogin: async (email: string, password: string) => {
    // Mock login: qualquer email na lista + qualquer senha funciona
    if (!MOCK_USERS[email]) {
      return Promise.reject({
        response: {
          status: 401,
          data: { message: 'Email não encontrado. Use: test@omnira.local ou admin@omnira.local' }
        }
      })
    }

    const token = generateMockToken(email)
    const user = MOCK_USERS[email]

    return Promise.resolve({
      data: {
        token,
        user,
        tenant: {
          id: '11111111-1111-1111-1111-111111111111',
          name: 'Test Company LTDA'
        }
      }
    })
  },
  logout: () => {
    localStorage.removeItem('token')
    localStorage.removeItem('user')
    return Promise.resolve({ data: { status: 'ok' } })
  },
  refresh: () => api.post('/v1/auth/refresh')
}

export const accountsAPI = {
  list: (params?: Record<string, any>) =>
    Promise.resolve({
      data: [
        {
          id: '11111111-1111-1111-1111-111111111111',
          name: 'Test Company LTDA',
          account_type: 'operator',
          status: 'active',
          created_at: '2026-09-01T10:00:00Z'
        },
        {
          id: '22222222-2222-2222-2222-222222222222',
          name: 'Support Center SP',
          account_type: 'contact_center',
          status: 'active',
          created_at: '2026-09-05T14:30:00Z'
        },
        {
          id: '33333333-3333-3333-3333-333333333333',
          name: 'Partner Reseller MG',
          account_type: 'reseller',
          status: 'active',
          created_at: '2026-09-10T09:15:00Z'
        },
        {
          id: '44444444-4444-4444-4444-444444444444',
          name: 'Old Account',
          account_type: 'operator',
          status: 'inactive',
          created_at: '2026-08-15T16:45:00Z'
        }
      ]
    }),

  get: (id: string) =>
    Promise.resolve({
      data: {
        id,
        name: 'Account Name',
        account_type: 'operator',
        status: 'active',
        sla_configuration: {
          first_response_target_hours: 2,
          resolution_target_hours: 24
        },
        created_at: '2026-09-01T10:00:00Z',
        updated_at: '2026-09-19T00:00:00Z'
      }
    }),

  create: (data: any) =>
    Promise.resolve({
      data: {
        id: `new-${Math.random()}`,
        ...data,
        created_at: new Date().toISOString()
      }
    }),

  update: (id: string, data: any) =>
    Promise.resolve({
      data: {
        id,
        ...data,
        updated_at: new Date().toISOString()
      }
    }),

  suspend: (id: string) =>
    Promise.resolve({
      data: {
        id,
        status: 'suspended',
        updated_at: new Date().toISOString()
      }
    }),

  reactivate: (id: string) =>
    Promise.resolve({
      data: {
        id,
        status: 'active',
        updated_at: new Date().toISOString()
      }
    }),

  metrics: (id: string) =>
    Promise.resolve({
      data: {
        account_id: id,
        total_tickets: 42,
        open_tickets: 5,
        sla_compliance: 96.5,
        average_resolution_time_hours: 18,
        last_7_days_tickets: 12
      }
    })
}

const MOCK_TICKETS = [
  {
    id: 'TKT-001',
    account_id: '11111111-1111-1111-1111-111111111111',
    title: 'Integração API com certificado SSL',
    priority: 'high',
    status: 'open',
    assigned_to: null,
    created_at: '2026-09-18T10:30:00Z',
    updated_at: '2026-09-19T08:00:00Z'
  },
  {
    id: 'TKT-002',
    account_id: '22222222-2222-2222-2222-222222222222',
    title: 'Melhorar performance de relatórios',
    priority: 'medium',
    status: 'in_progress',
    assigned_to: 'test@omnira.local',
    created_at: '2026-09-17T14:15:00Z',
    updated_at: '2026-09-19T09:30:00Z'
  },
  {
    id: 'TKT-003',
    account_id: '33333333-3333-3333-3333-333333333333',
    title: 'Resolver problema de autenticação LDAP',
    priority: 'critical',
    status: 'in_progress',
    assigned_to: 'admin@omnira.local',
    created_at: '2026-09-19T07:45:00Z',
    updated_at: '2026-09-19T10:15:00Z'
  },
  {
    id: 'TKT-004',
    account_id: '11111111-1111-1111-1111-111111111111',
    title: 'Adicionar suporte a webhook',
    priority: 'low',
    status: 'resolved',
    assigned_to: 'test@omnira.local',
    created_at: '2026-09-15T09:00:00Z',
    updated_at: '2026-09-18T16:30:00Z'
  },
  {
    id: 'TKT-005',
    account_id: '22222222-2222-2222-2222-222222222222',
    title: 'Documentação de API endpoint /metrics',
    priority: 'low',
    status: 'closed',
    assigned_to: null,
    created_at: '2026-09-10T11:20:00Z',
    updated_at: '2026-09-16T13:45:00Z'
  }
]

export const ticketsAPI = {
  list: (params?: Record<string, any>) =>
    Promise.resolve({
      data: MOCK_TICKETS.filter((t) => {
        if (params?.status && t.status !== params.status) return false
        if (params?.priority && t.priority !== params.priority) return false
        if (params?.account_id && t.account_id !== params.account_id) return false
        return true
      })
    }),

  get: (id: string) =>
    Promise.resolve({
      data: MOCK_TICKETS.find(t => t.id === id) || {
        id,
        account_id: '11111111-1111-1111-1111-111111111111',
        title: 'Ticket Padrão',
        priority: 'medium',
        status: 'open',
        assigned_to: null,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString()
      }
    }),

  create: (data: any) =>
    Promise.resolve({
      data: {
        id: `TKT-${String(MOCK_TICKETS.length + 1).padStart(3, '0')}`,
        ...data,
        status: 'open',
        assigned_to: null,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString()
      }
    }),

  update: (id: string, data: any) =>
    Promise.resolve({
      data: {
        id,
        ...MOCK_TICKETS.find(t => t.id === id),
        ...data,
        updated_at: new Date().toISOString()
      }
    }),

  assign: (id: string, userId: string) =>
    Promise.resolve({
      data: {
        id,
        assigned_to: userId,
        updated_at: new Date().toISOString()
      }
    }),

  resolve: (id: string) =>
    Promise.resolve({
      data: {
        id,
        status: 'resolved',
        updated_at: new Date().toISOString()
      }
    }),

  close: (id: string) =>
    Promise.resolve({
      data: {
        id,
        status: 'closed',
        updated_at: new Date().toISOString()
      }
    })
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

export const dashboardAPI = {
  getMetrics: () =>
    Promise.resolve({
      data: {
        totalAccounts: 5,
        openTickets: 12,
        slaCompliance: 94.5,
        activeAlerts: 2,
        ticketsByStatus: {
          open: 8,
          in_progress: 3,
          resolved: 1,
          closed: 0
        },
        accountsHealth: {
          active: 4,
          inactive: 1,
          suspended: 0
        },
        recentActivities: [
          { id: '1', action: 'Ticket criado', timestamp: new Date(Date.now() - 5 * 60000), actor: 'Test User' },
          { id: '2', action: 'Conta ativada', timestamp: new Date(Date.now() - 15 * 60000), actor: 'Admin User' },
          { id: '3', action: 'SLA report gerado', timestamp: new Date(Date.now() - 45 * 60000), actor: 'System' },
          { id: '4', action: 'Membership criada', timestamp: new Date(Date.now() - 2 * 3600000), actor: 'Admin User' },
          { id: '5', action: 'Tenant criado', timestamp: new Date(Date.now() - 24 * 3600000), actor: 'System' }
        ]
      }
    })
}

export default api
