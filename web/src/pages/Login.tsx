import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuthStore } from '../lib/store'
import { authAPI } from '../lib/api'
import { saveSession } from '../lib/session'

export default function Login() {
  const navigate = useNavigate()
  const { setUser, setToken } = useAuthStore()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
	const [mode, setMode] = useState<'mock' | 'oidc' | null>(null)

	useEffect(() => {
		let active = true
		const completeOIDC = new URLSearchParams(window.location.search).get('oidc') === 'complete'
		authAPI.mode().then(async ({ data }) => {
			if (!active) return
			setMode(data.mode)
			if (data.mode === 'oidc' && completeOIDC) {
				setLoading(true)
				try {
					const session = await authAPI.session()
					const { user, tenant } = session.data
					setUser({ ...user, roles: user.roles ?? [] })
					saveSession('', tenant?.id, user)
					// Se sem tenant, redirecionar para página de sem acesso
					if (!tenant || !tenant.id) {
						navigate('/no-access', { replace: true })
					} else {
						navigate('/', { replace: true })
					}
				} catch (err: any) {
					// Se erro ao buscar sessão (ex: sem tenant/membership), mostrar sem acesso
					if (err.response?.status === 403) {
						navigate('/no-access', { replace: true })
					} else {
						setError('A sessão do provedor de identidade não pôde ser validada.')
					}
				} finally {
					setLoading(false)
				}
			}
		}).catch(() => active && setError('Não foi possível consultar o modo de autenticação.'))
		return () => { active = false }
	}, [navigate, setUser])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    try {
      const login = import.meta.env.VITE_MOCK_AUTH === 'true' ? authAPI.mockLogin : authAPI.login
      const response = await login(email, password)
      const { token, user, tenant } = response.data

		const offlineMock = import.meta.env.VITE_MOCK_AUTH === 'true'

		if (!user) {
        setError('Login retornou dados inválidos')
        return
      }

		if (offlineMock) setToken(token)
      setUser(user)
		saveSession(offlineMock ? token : '', tenant?.id, user)

      // Aguarda um momento para garantir que o state foi atualizado
      setTimeout(() => navigate('/'), 100)
    } catch (err: any) {
      console.error('Login error:', err)

      if (err.response?.data?.message) {
        setError(err.response.data.message)
      } else if (err.message) {
        setError(err.message)
      } else {
        setError('Erro ao fazer login. Verifique o email.')
      }
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen bg-gradient-to-br from-blue-600 to-blue-800 flex items-center justify-center p-4">
      <div className="bg-white rounded-xl shadow-2xl p-8 w-full max-w-md">
        <div className="text-center mb-8">
          <h1 className="text-4xl font-bold text-slate-900">OMNIRA</h1>
          <p className="text-slate-600 mt-2">Plataforma SaaS Empresarial</p>
        </div>

        {error && (
          <div className="bg-red-50 border border-red-200 text-red-700 px-4 py-3 rounded-lg mb-6">
            {error}
          </div>
        )}

		{mode === 'oidc' ? (
			<button className="w-full btn-primary font-medium" disabled={loading} onClick={() => authAPI.startOIDC()}>
				{loading ? 'Validando sessão...' : 'Entrar com o provedor de identidade'}
			</button>
		) : mode === 'mock' || import.meta.env.VITE_MOCK_AUTH === 'true' ? (
		<form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-slate-700 mb-1">
              Email
            </label>
            <input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="w-full px-4 py-2 border border-slate-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="seu@email.com"
              required
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-slate-700 mb-1">
              Senha
            </label>
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="w-full px-4 py-2 border border-slate-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="••••••••"
              required
            />
          </div>

          <button
            type="submit"
            disabled={loading}
            className="w-full btn-primary font-medium"
          >
            {loading ? 'Autenticando...' : 'Entrar'}
          </button>
		</form>
		) : <div className="text-center text-slate-500">Carregando autenticação...</div>}

        <div className="mt-6 pt-6 border-t border-slate-200 text-center text-sm text-slate-600">
          <p>Demo: use credenciais válidas do seu servidor</p>
        </div>
      </div>
    </div>
  )
}
