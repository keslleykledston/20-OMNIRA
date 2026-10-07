import { Outlet, useNavigate } from 'react-router-dom'
import Sidebar from './Sidebar'
import Header from './Header'
import MobileNav from './MobileNav'
import { useEffect } from 'react'
import { hasSession } from '../lib/session'
import { LoadingState } from './primitives'
import { usePresenceHeartbeat } from '../hooks/usePresenceHeartbeat'

export default function Layout() {
  const navigate = useNavigate()
  const authenticated = hasSession()

  useEffect(() => {
    if (!authenticated) {
      navigate('/login', { replace: true })
    }
  }, [authenticated, navigate])

  // ADR-0010: any authenticated session heartbeats; the backend silently
  // ignores it (403) when the user has no active AgentProfile.
  usePresenceHeartbeat(authenticated)

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
