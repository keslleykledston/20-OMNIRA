import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuthStore } from '../lib/store'
import { authAPI, type AuthMode } from '../lib/api'
import { saveSession } from '../lib/session'
import { Button, Icon, Input } from '../components/primitives'

type Status =
  | { kind: 'loading' }
  | { kind: 'ready' }
  | { kind: 'redirecting' }
  | { kind: 'error'; message: string }

// O IdP devolve mensagens cruas que expõem infraestrutura e não ajudam quem
// está na tela; o usuário vê sempre o mesmo texto e o detalhe fica no console.
const OIDC_ERROR = 'Não foi possível concluir o login. Tente novamente ou contate o administrador.'

export default function Login() {
  const navigate = useNavigate()
  const { setUser, setToken } = useAuthStore()

  const [auth, setAuth] = useState<AuthMode | null>(null)
  const [status, setStatus] = useState<Status>({ kind: 'loading' })
  const [sessionExpired, setSessionExpired] = useState(false)

  useEffect(() => {
    let active = true
    const params = new URLSearchParams(window.location.search)
    setSessionExpired(params.get('reason') === 'session_expired')
    const completeOIDC = params.get('oidc') === 'complete'

    authAPI
      .mode()
      .then(async ({ data }) => {
        if (!active) return
        setAuth(data)

        if (data.mode === 'oidc' && completeOIDC) {
          try {
            const session = await authAPI.session()
            const { user, tenant } = session.data
            setUser({ ...user, roles: user.roles ?? [] })
            saveSession('', tenant?.id, user)
            navigate(tenant?.id ? '/' : '/no-access', { replace: true })
            return
          } catch (err: any) {
            if (!active) return
            if (err.response?.status === 403) {
              navigate('/no-access', { replace: true })
              return
            }
            console.error('OIDC session error:', err)
            setStatus({ kind: 'error', message: OIDC_ERROR })
            return
          }
        }
        setStatus({ kind: 'ready' })
      })
      .catch(() => {
        if (!active) return
        setStatus({ kind: 'error', message: 'Não foi possível consultar os métodos de acesso.' })
      })

    return () => {
      active = false
    }
  }, [navigate, setUser])

  const startSSO = () => {
    setStatus({ kind: 'redirecting' })
    authAPI.startOIDC()
  }

  const busy = status.kind === 'loading' || status.kind === 'redirecting'

  return (
    <div className="min-h-screen bg-canvas flex flex-col items-center justify-center px-4 py-12">
      <main className="w-full max-w-[400px]">
        <div className="rounded-card bg-surface shadow-md border border-border-subtle p-8">
          <div className="text-center mb-8">
            <h1 className="text-display-md font-semibold text-text-primary">OMNIRA</h1>
            <p className="text-text-secondary mt-2">Entre na sua conta</p>
          </div>

          {sessionExpired && (
            <div
              role="status"
              className="mb-6 rounded-control border border-border-subtle bg-surface-muted px-4 py-3 text-sm text-text-secondary"
            >
              Sua sessão expirou. Entre novamente para continuar.
            </div>
          )}

          {status.kind === 'error' && (
            <div
              role="alert"
              className="mb-6 rounded-control border border-status-danger-border bg-status-danger-soft px-4 py-3 text-sm text-text-primary"
            >
              {status.message}
            </div>
          )}

          {auth?.mode === 'unavailable' && status.kind !== 'error' && (
            <div
              role="alert"
              className="rounded-control border border-status-warning-border bg-status-warning-soft px-4 py-3 text-sm text-text-primary"
            >
              Nenhum método de autenticação está configurado neste ambiente.
            </div>
          )}

          {auth?.mode === 'oidc' && (
            <>
              <Button
                variant="primary"
                size="lg"
                className="w-full"
                onClick={startSSO}
                disabled={busy}
              >
                {status.kind === 'redirecting' ? 'Redirecionando…' : 'Continuar com SSO'}
              </Button>
              <p className="mt-4 text-center text-sm text-text-secondary">
                Use sua conta corporativa para acessar o OMNIRA.
              </p>
            </>
          )}

          {/* Sem OIDC o card ficaria só com o logo e um espaço vazio; dizer que
              o acesso corporativo não está configurado explica a ausência do
              botão em vez de deixar o operador procurando por ele. */}
          {auth?.mode === 'dev' && (
            <p className="text-center text-sm text-text-secondary">
              O acesso corporativo (SSO) não está configurado neste ambiente.
            </p>
          )}
        </div>

        {auth?.dev_auth && <DevLoginCard />}
      </main>
    </div>
  )
}

// Bloco separado e subordinado ao card principal: é ferramenta de
// desenvolvimento, não um segundo caminho de login. Só aparece quando o
// servidor informa que a rota existe — nunca por hostname ou query param.
function DevLoginCard() {
  const navigate = useNavigate()
  const { setUser, setToken } = useAuthStore()
  const [email, setEmail] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      const { data } = await authAPI.devLogin(email.trim())
      if (!data.user) {
        setError('O acesso de desenvolvimento retornou dados inválidos.')
        return
      }
      setToken(data.token)
      setUser(data.user)
      saveSession(data.token, data.tenant?.id, data.user)
      navigate('/', { replace: true })
    } catch (err: any) {
      // 404 significa que a rota não está registrada neste servidor.
      setError(
        err.response?.status === 404
          ? 'O acesso de desenvolvimento não está disponível neste servidor.'
          : 'E-mail não reconhecido para acesso de desenvolvimento.',
      )
    } finally {
      setLoading(false)
    }
  }

  return (
    <section className="mt-4 rounded-card border border-status-warning-border bg-status-warning-soft p-6">
      <div className="flex items-center gap-2 mb-1">
        <Icon name="info" size={16} className="text-status-warning-strong" />
        <h2 className="text-sm font-semibold text-status-warning-strong">Modo de desenvolvimento</h2>
      </div>
      <p className="text-sm text-text-secondary mb-4">
        Este acesso existe somente para testes locais e de laboratório.
      </p>

      <form onSubmit={submit} className="space-y-3">
        <div>
          <label htmlFor="dev-email" className="block text-sm font-medium text-text-primary mb-1">
            E-mail
          </label>
          {/* autocomplete ligado de propósito: WCAG 2.2 pede que a autenticação
              não dependa de o usuário decorar e digitar tudo à mão, e aqui não
              há segredo a proteger. */}
          <Input
            id="dev-email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoComplete="email"
          />
        </div>

        {error && (
          <p role="alert" className="text-sm text-status-danger">
            {error}
          </p>
        )}

        <Button type="submit" variant="secondary" size="md" className="w-full" disabled={loading}>
          {loading ? 'Entrando…' : 'Entrar como usuário de teste'}
        </Button>
      </form>
    </section>
  )
}
