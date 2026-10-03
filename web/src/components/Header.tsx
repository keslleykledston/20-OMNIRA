import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuthStore } from '../lib/store'
import { authAPI } from '../lib/api'
import { Button, Avatar } from './primitives'
import TenantSwitcher from './TenantSwitcher'
import clsx from 'clsx'

export default function Header() {
  const navigate = useNavigate()
  const { user, logout, setUser } = useAuthStore()

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
    const { data } = await authAPI.logout()
    logout()
    // Ending the identity provider's session too is what lets someone switch accounts.
    if (data.endSessionUrl) window.location.assign(data.endSessionUrl)
    else navigate('/login')
  }

  return (
    <header
      style={{ height: 'var(--header-height)' }}
      className={clsx(
        // On desktop the reference shows no shell header band: blend into the canvas.
        // On mobile this bar is the only chrome (sidebar hidden), so keep it a surface.
        'bg-surface lg:bg-transparent',
        'border-b border-border-subtle lg:border-b-0',
        'px-4 sm:px-6',
        'flex',
        'items-center',
        'justify-between',
        'sticky',
        'top-0',
        'z-10'
      )}
    >
      {/* Brand only where the sidebar is hidden; on desktop it lives in the sidebar. */}
      <div className="flex items-center gap-4 lg:invisible">
        <span className="text-lg font-bold text-text-primary">OMNIRA</span>
      </div>

      <div className="flex min-w-0 items-center gap-2 sm:gap-4">
        <TenantSwitcher />
        {user && (
          <div className="flex items-center gap-2 sm:gap-3">
            <div className="hidden sm:block">
              <p className="text-sm font-medium text-text-primary">
                {user.name}
              </p>
              <p className="hidden text-xs text-text-tertiary sm:block">
                {user.email}
              </p>
            </div>
            <Avatar alt={user.name} initials={user.name?.substring(0, 2).toUpperCase()} size="md" />
          </div>
        )}
        <Button
          onClick={handleLogout}
          variant="secondary"
          size="sm"
        >
          Sair
        </Button>
      </div>
    </header>
  )
}
