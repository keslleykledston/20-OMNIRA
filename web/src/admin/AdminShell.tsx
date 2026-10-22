import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import clsx from 'clsx'
import { BrandMark } from '../components/BrandMark'
import { Button, EmptyState, LoadingState } from '../components/primitives'
import { authAPI } from '../lib/api'
import { isActing } from '../lib/acting'
import { hasSession, USER_KEY } from '../lib/session'
import { useAuthStore } from '../lib/store'
import { useMyHubs } from '../hooks/useMyHubs'
import { usePlatformOperator } from '../hooks/usePlatformOperator'
import { rememberAfterLogin } from '../lib/afterLogin'

// Console de administração (ADR-0041): agentes, empresas (instâncias) e canais geridos pelo Hub. É uma entrada própria do build (admin.html, servida em
// /admin/): o bundle do agente não contém estas telas. A guarda abaixo só decide o que MOSTRAR; o servidor decide cada requisição de novo.
function savedUserName(): string {
  try {
    const u = JSON.parse(localStorage.getItem(USER_KEY) || 'null') as { name?: string; email?: string } | null
    return u?.name || u?.email || ''
  } catch {
    return ''
  }
}

const tab = ({ isActive }: { isActive: boolean }) =>
  clsx(
    'rounded-control px-3 py-2 text-sm no-underline transition-colors',
    isActive ? 'bg-accent-primary-soft font-semibold text-accent-primary' : 'text-text-secondary hover:bg-surface-muted hover:text-text-primary',
  )

export default function AdminShell() {
  const location = useLocation()
  const hubs = useMyHubs()
  // Platform operator (ADR-0038) is a SEPARATE authority from Hub grants — a person can be one
  // without the other. Probed the same way as the Hub tabs: a real call decides what to show.
  const platformOperator = usePlatformOperator()
  const logout = useAuthStore((s) => s.logout)
  const [signedIn] = useState(hasSession())
  const acting = isActing()

  useEffect(() => {
    if (!signedIn) {
      rememberAfterLogin(window.location.pathname + window.location.search)
      window.location.replace('/login')
    } else if (acting) {
      // Attending an instance through the Hub has only the conversations (ADR-0040); administration is not offered there.
      window.location.replace('/inbox')
    }
  }, [signedIn, acting])

  const signOut = async () => {
    const res = await authAPI.logout()
    logout()
    if (res.data?.endSessionUrl) window.location.assign(res.data.endSessionUrl)
    else window.location.assign('/login?reason=signed_out')
  }

  if (!signedIn || acting) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-canvas">
        <LoadingState message="Verificando sessão..." />
      </div>
    )
  }

  const list = hubs.data ?? []
  const canAccess = list.some((h) => h.can_manage_access)
  const canManage = list.some((h) => h.can_manage_instances)
  const entitled = canAccess || canManage || platformOperator.isOperator

  return (
    <div className="flex min-h-dvh flex-col bg-canvas">
      <header className="sticky top-0 z-10 flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-border-subtle bg-surface px-4 py-2 sm:px-6">
        <div className="flex items-center gap-2">
          <BrandMark size={28} />
          <span className="text-lg font-bold text-text-primary">OMNIRA</span>
          <span className="rounded-control bg-surface-muted px-2 py-0.5 text-xs font-medium text-text-secondary">Administração</span>
        </div>
        <nav aria-label="Console de administração" className="flex flex-wrap items-center gap-1">
          {canAccess && (
            <NavLink to="/acessos" className={tab}>
              Acessos
            </NavLink>
          )}
          {canManage && (
            <NavLink to="/instancias" className={tab}>
              Canais das instâncias
            </NavLink>
          )}
          {platformOperator.isOperator && (
            <NavLink to="/plataforma" className={tab}>
              Plataforma
            </NavLink>
          )}
        </nav>
        <div className="ml-auto flex items-center gap-3">
          <span className="hidden text-sm text-text-secondary sm:inline">{savedUserName()}</span>
          <a href="/inbox" className="text-sm font-medium text-accent-primary underline-offset-2 hover:underline">
            Voltar ao atendimento
          </a>
          <Button variant="secondary" size="sm" onClick={signOut}>
            Sair
          </Button>
        </div>
      </header>
      <main className="min-h-0 flex-1" data-route={location.pathname}>
        {hubs.isLoading || platformOperator.isLoading ? (
          <LoadingState message="Carregando…" />
        ) : !entitled ? (
          <div className="mx-auto max-w-xl p-6">
            <EmptyState
              title="Sem acesso ao console"
              description="O console é para administradores de um Hub, para quem recebeu a gestão de canais de uma instância e para operadores de plataforma. Se você deveria ter acesso, fale com o administrador do Hub ou com um operador de plataforma."
            />
          </div>
        ) : (
          // Repassa o MESMO resultado de usePlatformOperator já obtido aqui (contexto do Outlet, não
          // um segundo useQuery): achado real em navegador — uma SEGUNDA assinatura da mesma
          // chave, montada só quando esta guarda libera o Outlet, disparava seu próprio
          // refetchOnMount; enquanto ele estava em voo esta MESMA guarda via isLoading=true de
          // novo, desmontava o Outlet (e a assinatura nova com ele), o que deixava a tela
          // alternando montar/desmontar para sempre. Nenhum teste em jsdom reproduziu isso.
          <Outlet context={platformOperator} />
        )}
      </main>
    </div>
  )
}
