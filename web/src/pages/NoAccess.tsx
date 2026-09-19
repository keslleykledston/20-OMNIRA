import { useNavigate } from 'react-router-dom'
import { authAPI } from '../lib/api'

export default function NoAccess() {
  const navigate = useNavigate()

  const handleLogout = async () => {
    try {
      await authAPI.logout()
    } finally {
      navigate('/login', { replace: true })
    }
  }

  return (
    <div className="min-h-screen bg-gradient-to-br from-blue-600 to-blue-800 flex items-center justify-center p-4">
      <div className="bg-white rounded-xl shadow-2xl p-8 w-full max-w-md text-center">
        <div className="mb-6">
          <div className="text-6xl text-slate-300 mb-4">🔒</div>
          <h1 className="text-2xl font-bold text-slate-900">Sem Acesso</h1>
        </div>

        <p className="text-slate-600 mb-6">
          Sua conta foi autenticada, mas você ainda não possui acesso a nenhuma organização.
        </p>

        <p className="text-sm text-slate-500 mb-8 bg-blue-50 p-4 rounded-lg">
          Entre em contato com o administrador da sua organização para obter acesso.
        </p>

        <button
          onClick={handleLogout}
          className="w-full btn-primary font-medium"
        >
          Sair
        </button>
      </div>
    </div>
  )
}
