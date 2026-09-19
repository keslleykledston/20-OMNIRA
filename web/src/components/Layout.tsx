import { Outlet, useNavigate } from 'react-router-dom'
import { useAuthStore, useUIStore } from '../lib/store'
import Sidebar from './Sidebar'
import Header from './Header'
import { useEffect } from 'react'

export default function Layout() {
  const navigate = useNavigate()
  const { token } = useAuthStore()
  const { sidebarOpen } = useUIStore()

  useEffect(() => {
    if (!token) {
      navigate('/login', { replace: true })
    }
  }, [token, navigate])

  if (!token) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-slate-100">
        <div className="text-center">
          <div className="text-slate-500">Redirecionando para login...</div>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-screen bg-slate-100">
      <Sidebar />
      <div className="flex-1 flex flex-col overflow-hidden">
        <Header />
        <main className="flex-1 overflow-auto">
          <div className={`transition-all duration-300 ${sidebarOpen ? 'ml-0' : '-ml-64'}`}>
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
