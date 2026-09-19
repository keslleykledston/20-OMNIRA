import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuthStore, useUIStore } from '../lib/store'
import { authAPI } from '../lib/api'

export default function Header() {
  const navigate = useNavigate()
  const { user, logout, setUser } = useAuthStore()
  const { toggleSidebar } = useUIStore()

  useEffect(() => {
    // Carregar user do localStorage se não estiver no store
    if (!user) {
      const savedUser = localStorage.getItem('user')
      if (savedUser) {
        try {
          setUser(JSON.parse(savedUser))
        } catch (e) {
          console.error('Failed to load user:', e)
        }
      }
    }
  }, [user, setUser])

  const handleLogout = async () => {
	await authAPI.logout()
    logout()
    navigate('/login')
  }

  return (
    <header className="bg-white border-b border-slate-200 px-6 py-4 flex justify-between items-center">
      <div className="flex items-center gap-4">
        <button
          onClick={toggleSidebar}
          className="p-2 hover:bg-slate-100 rounded-lg transition-colors"
        >
          <svg className="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 6h16M4 12h16M4 18h16" />
          </svg>
        </button>
        <h1 className="text-2xl font-bold text-slate-900">OMNIRA</h1>
      </div>

      <div className="flex items-center gap-4">
        <div className="text-sm text-slate-600">
          {user?.name}
        </div>
        <button
          onClick={handleLogout}
          className="btn-secondary text-sm"
        >
          Sair
        </button>
      </div>
    </header>
  )
}
