import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Button } from '../components/primitives'
import { authAPI } from '../lib/api'
import { invitationAcceptAPI, type AcceptStatus } from '../lib/invitations'
import { displayRoleName } from '../lib/roles'

type ViewState =
  | { kind: 'loading' }
  | { kind: 'needs-auth'; info: InvitePreview }
  | { kind: 'ready'; info: InvitePreview }
  | { kind: 'accepting' }
  | { kind: 'accepted' }
  | { kind: 'terminal'; status: AcceptStatus }
  | { kind: 'error' }

interface InvitePreview {
  tenantName?: string
  roleName?: string
  maskedEmail?: string
}

// return path após o SSO fica no state do OAuth (parâmetro state assinado pelo
// backend, não em localStorage) — o token do convite é o único dado que
// precisa sobreviver ao redirect, e vai na própria URL de retorno, que é
// allowlisted pelo backend (mesma origem, path fixo /invite/:token).
export default function AcceptInvitePage() {
  const { token = '' } = useParams()
  const navigate = useNavigate()
  const [state, setState] = useState<ViewState>({ kind: 'loading' })
  const [accepting, setAccepting] = useState(false)

  useEffect(() => {
    let active = true
    // GET .../status exige sessão autenticada; um 401 aqui É a informação de
    // que falta login, não algo lido de um store local que pode estar
    // desatualizado após um redirect completo de página (volta do OIDC).
    invitationAcceptAPI
      .status(token)
      .then((data) => {
        if (!active) return
        const info: InvitePreview = {
          tenantName: data.tenant_name,
          roleName: displayRoleName(data.role_key, data.role_name ?? ''),
          maskedEmail: data.masked_email,
        }
        setState(data.status === 'pending' ? { kind: 'ready', info } : { kind: 'terminal', status: data.status })
      })
      .catch((err) => {
        if (!active) return
        if (err.response?.status === 401) {
          setState({ kind: 'needs-auth', info: {} })
        } else {
          setState({ kind: 'error' })
        }
      })
    return () => {
      active = false
    }
  }, [token])

  const startSSO = () => {
    // O backend valida o retorno contra uma allowlist (só /invite/:token) —
    // nunca aceita uma URL de retorno arbitrária.
    authAPI.startOIDC(`/invite/${token}`)
  }

  const accept = async () => {
    setAccepting(true)
    try {
      await invitationAcceptAPI.accept(token)
      setState({ kind: 'accepted' })
      setTimeout(() => navigate('/', { replace: true }), 1500)
    } catch (err: any) {
      const status = err.response?.status
      if (status === 409) setState({ kind: 'terminal', status: 'accepted' })
      else if (status === 410) setState({ kind: 'terminal', status: 'expired' })
      else if (status === 404) setState({ kind: 'terminal', status: 'not_found' })
      else setState({ kind: 'error' })
    } finally {
      setAccepting(false)
    }
  }

  return (
    <div className="min-h-screen bg-canvas flex flex-col items-center justify-center px-4 py-12">
      <main className="w-full max-w-[440px]">
        <div className="rounded-card bg-surface shadow-md border border-border-subtle p-8 text-center">
          <h1 className="text-display-md font-semibold text-text-primary mb-6">OMNIRA</h1>

          {state.kind === 'loading' && <p className="text-text-secondary">Verificando convite…</p>}

          {state.kind === 'needs-auth' && (
            <>
              {inviteSummary(state.info)}
              <Button variant="primary" size="lg" className="w-full mt-2" onClick={startSSO}>
                Continuar com SSO
              </Button>
            </>
          )}

          {state.kind === 'ready' && (
            <>
              {inviteSummary(state.info)}
              <Button variant="primary" size="lg" className="w-full mt-2" onClick={accept} disabled={accepting}>
                {accepting ? 'Entrando…' : 'Aceitar convite'}
              </Button>
            </>
          )}

          {state.kind === 'accepted' && (
            <p role="status" className="text-status-success">
              Convite aceito! Redirecionando…
            </p>
          )}

          {state.kind === 'terminal' && <TerminalMessage status={state.status} />}

          {state.kind === 'error' && (
            <p role="alert" className="text-status-danger">
              Não foi possível verificar este convite. Tente novamente ou contate o administrador.
            </p>
          )}
        </div>
      </main>
    </div>
  )
}

function inviteSummary(info: InvitePreview) {
  if (!info.tenantName) return null
  return (
    <p className="text-text-secondary mb-6">
      Você foi convidado para <strong className="text-text-primary">{info.tenantName}</strong> como{' '}
      <strong className="text-text-primary">{info.roleName}</strong>
      {info.maskedEmail && <> ({info.maskedEmail})</>}.
    </p>
  )
}

function TerminalMessage({ status }: { status: AcceptStatus }) {
  const messages: Record<AcceptStatus, string> = {
    pending: '',
    accepted: 'Este convite já foi aceito.',
    revoked: 'Este convite foi revogado.',
    expired: 'Este convite expirou.',
    wrong_identity:
      'Este convite foi enviado para outro e-mail. Saia e entre novamente com a conta correta.',
    not_found: 'Convite não encontrado.',
  }
  return (
    <p role="alert" className="text-text-secondary">
      {messages[status]}
    </p>
  )
}
