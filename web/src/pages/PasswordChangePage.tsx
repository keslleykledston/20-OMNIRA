import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { authAPI } from '../lib/api'
import { useAuthStore } from '../lib/store'
import { Button, Icon, Input } from '../components/primitives'

type Status =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'success' }

interface PasswordChangeRequest {
  old_password: string
  new_password: string
}

export default function PasswordChangePage() {
  const navigate = useNavigate()
  const { user } = useAuthStore()
  const [status, setStatus] = useState<Status>({ kind: 'idle' })
  const [oldPassword, setOldPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')

  const isForcedChange = !user?.password_expires_at || new Date(user.password_expires_at) > new Date()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setStatus({ kind: 'loading' })

    // Validações básicas
    if (!newPassword || newPassword.length < 8) {
      setStatus({ kind: 'error', message: 'Senha deve ter pelo menos 8 caracteres' })
      return
    }

    if (newPassword !== confirmPassword) {
      setStatus({ kind: 'error', message: 'Senhas não conferem' })
      return
    }

    if (!isForcedChange && !oldPassword) {
      setStatus({ kind: 'error', message: 'Senha atual é obrigatória' })
      return
    }

    try {
      const payload: PasswordChangeRequest = {
        old_password: oldPassword,
        new_password: newPassword
      }

      const response = await authAPI.changePassword(payload)
      if (response.status === 200) {
        setStatus({ kind: 'success' })
        // Redirecionar para inbox após sucesso
        setTimeout(() => navigate('/inbox'), 1500)
      }
    } catch (err: any) {
      const errorMsg = err.response?.data?.error || err.response?.data?.message || 'Erro ao alterar senha'
      setStatus({ kind: 'error', message: errorMsg })
    }
  }

  return (
    <div className="min-h-screen bg-gradient-to-br from-blue-50 to-indigo-100 flex items-center justify-center p-4">
      <div className="bg-white rounded-lg shadow-xl max-w-md w-full p-8">
        {/* Header */}
        <div className="text-center mb-8">
          <div className="inline-flex items-center justify-center w-12 h-12 bg-indigo-100 rounded-full mb-4">
            <Icon name="settings" className="w-6 h-6 text-indigo-600" />
          </div>
          <h1 className="text-2xl font-bold text-gray-900">
            {isForcedChange ? 'Alterar Senha' : 'Alterar Senha'}
          </h1>
          <p className="text-gray-600 mt-2">
            {isForcedChange
              ? 'Você precisa alterar sua senha antes de continuar'
              : 'Atualize sua senha por segurança'}
          </p>
        </div>

        {/* Status Messages */}
        {status.kind === 'error' && (
          <div className="mb-6 p-4 bg-red-50 border border-red-200 rounded-lg">
            <p className="text-red-700 text-sm font-medium">{status.message}</p>
          </div>
        )}

        {status.kind === 'success' && (
          <div className="mb-6 p-4 bg-green-50 border border-green-200 rounded-lg">
            <p className="text-green-700 text-sm font-medium">✓ Senha alterada com sucesso! Redirecionando...</p>
          </div>
        )}

        {/* Form */}
        <form onSubmit={handleSubmit} className="space-y-4">
          {/* Old Password (se não é troca forçada) */}
          {!isForcedChange && (
            <div>
              <label className="block text-sm font-medium text-gray-700 mb-2">
                Senha Atual
              </label>
              <Input
                type="password"
                placeholder="Digite sua senha atual"
                value={oldPassword}
                onChange={(e) => setOldPassword(e.target.value)}
                disabled={status.kind === 'loading' || status.kind === 'success'}
              />
            </div>
          )}

          {/* New Password */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-2">
              Nova Senha
            </label>
            <Input
              type="password"
              placeholder="Digite a nova senha (mín. 8 caracteres)"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              disabled={status.kind === 'loading' || status.kind === 'success'}
            />
            <p className="text-xs text-gray-500 mt-1">
              Mínimo 8 caracteres. Use letras, números e símbolos.
            </p>
          </div>

          {/* Confirm Password */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-2">
              Confirmar Senha
            </label>
            <Input
              type="password"
              placeholder="Confirme a nova senha"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              disabled={status.kind === 'loading' || status.kind === 'success'}
            />
          </div>

          {/* Submit Button */}
          <Button
            type="submit"
            disabled={status.kind === 'loading' || status.kind === 'success'}
            className="w-full mt-6"
          >
            {status.kind === 'loading' ? 'Processando...' : 'Alterar Senha'}
          </Button>
        </form>

        {/* Info */}
        {isForcedChange && (
          <div className="mt-6 p-4 bg-blue-50 border border-blue-200 rounded-lg">
            <p className="text-blue-700 text-xs">
              <strong>Atenção:</strong> Você deve alterar sua senha para acessar a plataforma. Esta é uma troca obrigatória após o login inicial.
            </p>
          </div>
        )}
      </div>
    </div>
  )
}
