import { Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { isActing } from '../lib/acting'
import Sidebar from './Sidebar'
import Header from './Header'
import MobileNav from './MobileNav'
import { useEffect } from 'react'
import { hasSession } from '../lib/session'
import { LoadingState } from './primitives'
import { usePresenceHeartbeat } from '../hooks/usePresenceHeartbeat'

export default function Layout() {
  const navigate = useNavigate()
  const location = useLocation()
  const authenticated = hasSession()

  useEffect(() => {
    if (!authenticated) {
      navigate('/login', { replace: true })
    }
  }, [authenticated, navigate])

  // ADR-0010: any authenticated session heartbeats; the backend silently
  // ignores it (403) when the user has no active AgentProfile. Not while attending an instance through the Hub (ADR-0040): presence is the
  // member's own, and a Hub agent has none in that instance (the request would only be refused).
  usePresenceHeartbeat(authenticated && !isActing())

  // The shell is exactly one screen tall and scrolls INSIDE its panes. Without this lock the document itself can become
  // scrollable (anything appended to <body>, an overscroll, a focus jump), and scrolling it slides the whole app up and
  // shows an empty band under it.
  useEffect(() => {
    if (!authenticated) return
    document.documentElement.classList.add('app-shell-lock')
    return () => document.documentElement.classList.remove('app-shell-lock')
  }, [authenticated])

  if (!authenticated) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-canvas">
        <LoadingState message="Verificando sessão..." />
      </div>
    )
  }

  // Attending an instance through the Hub, only the conversations exist (ADR-0040 phase 03): any other address goes back to them.
  if (isActing() && location.pathname !== '/inbox') return <Navigate to="/inbox" replace />

  return (
    <div className="h-dvh overflow-clip bg-canvas flex flex-col lg:flex-row">
      {/* Desktop Sidebar */}
      <div className="hidden lg:block">
        <Sidebar />
      </div>

      {/* Main Content */}
      <div className="min-h-0 flex-1 flex flex-col overflow-clip">
        <Header />
        {/* pb-16 keeps content clear of the fixed mobile nav */}
        <main className="min-h-0 flex-1 overflow-auto pb-16 lg:pb-0">
          <Outlet />
        </main>
      </div>

      <MobileNav />
    </div>
  )
}
