import { Outlet, useNavigate } from 'react-router-dom'
import Sidebar from './Sidebar'
import Header from './Header'
import MobileNav from './MobileNav'
import { useEffect } from 'react'
import { hasSession } from '../lib/session'
import { LoadingState } from './primitives'

export default function Layout() {
  const navigate = useNavigate()
  const authenticated = hasSession()

  useEffect(() => {
    if (!authenticated) {
      navigate('/login', { replace: true })
    }
  }, [authenticated, navigate])

  if (!authenticated) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-canvas">
        <LoadingState message="Verificando sessão..." />
      </div>
    )
  }

  return (
    <div className="h-screen bg-canvas flex flex-col lg:flex-row">
      {/* Desktop Sidebar */}
      <div className="hidden lg:block">
        <Sidebar />
      </div>

      {/* Main Content */}
      <div className="flex-1 flex flex-col overflow-hidden">
        <Header />
        {/* pb-16 keeps content clear of the fixed mobile nav */}
        <main className="flex-1 overflow-auto pb-16 lg:pb-0">
          <Outlet />
        </main>
      </div>

      <MobileNav />
    </div>
  )
}
