import { Link, useLocation } from 'react-router-dom'
import clsx from 'clsx'
import { Avatar, Icon } from './primitives'
import type { IconName } from './primitives/Icon'
import { useTenantDisplay } from '../lib/tenantContext'
import { isDevSurface } from './UnavailableSurface'

// Only routes that exist in App.tsx. Spec items without a route (Automação, o
// restante de Configurações) stay hidden rather than simulated —
// docs/architecture/FRONTEND-UX.md. Equipe e acesso é a única seção de
// Configurações implementada até agora (IAM2A).
//
// mockBacked: destination has no real backend behind it (Dashboard/Tickets/
// Relatórios/Supervisor — DESIGN.5 reality gate, FRONTEND.1). Filtered out of
// operational navigation outside development so it never implies active
// production capability; the route itself still exists (see UnavailableSurface).
const navItems: { label: string; path: string; icon: IconName; alsoActiveOn?: string; mockBacked?: boolean }[] = [
  { label: 'Dashboard', path: '/', icon: 'dashboard', mockBacked: true },
  { label: 'Conversas', path: '/inbox', icon: 'conversations' },
  { label: 'Tickets', path: '/tickets', icon: 'tickets', mockBacked: true },
  { label: 'Contatos', path: '/contacts', icon: 'contacts' },
  { label: 'Canais', path: '/channels', icon: 'channels' },
  { label: 'Relatórios', path: '/reports', icon: 'reports', mockBacked: true },
  { label: 'Supervisor', path: '/supervisor', icon: 'supervisor', mockBacked: true },
  { label: 'Equipe e acesso', path: '/settings/team', icon: 'settings', alsoActiveOn: '/settings/roles' },
  { label: 'Agentes', path: '/settings/agents', icon: 'supervisor' },
]

export default function Sidebar() {
  const location = useLocation()
  const tenant = useTenantDisplay()
  const visibleNavItems = navItems.filter((item) => !item.mockBacked || isDevSurface())

  const isActive = (path: string) =>
    path === '/' ? location.pathname === '/' : location.pathname === path || location.pathname.startsWith(path + '/')

  return (
    <aside
      className={clsx(
        // Compact (icon-only) on tablet, full width from xl up — 01-FOUNDATION/APP-SHELL.md
        'w-sidebar-compact xl:w-sidebar',
        'bg-surface',
        'border-r',
        'border-border-subtle',
        'flex',
        'flex-col',
        'overflow-hidden',
        'sticky',
        'top-0',
        'h-screen'
      )}
    >
      {/* Logo/Branding */}
      <div className="px-4 xl:px-6 py-6 border-b border-border-subtle">
        <div className="flex items-center gap-2 justify-center xl:justify-start">
          <div className="w-8 h-8 bg-accent-primary rounded-card flex items-center justify-center text-white font-bold text-sm">
            O
          </div>
          <span className="hidden xl:inline font-bold text-text-primary text-lg">OMNIRA</span>
        </div>
      </div>

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto px-3 py-4 space-y-1">
        {visibleNavItems.map(item => {
          const active = isActive(item.path) || (item.alsoActiveOn ? isActive(item.alsoActiveOn) : false)
          return (
            <Link
              key={item.path}
              to={item.path}
              className={clsx(
                'flex',
                'items-center',
                'gap-3',
                'justify-center xl:justify-start',
                'px-3',
                'py-2.5',
                'rounded-control',
                'transition-colors',
                'text-sm',
                'no-underline',
                active
                  ? clsx(
                    'bg-accent-primary-soft',
                    'text-accent-primary',
                    'font-semibold'
                  )
                  : clsx(
                    'text-text-secondary',
                    'hover:bg-surface-muted',
                    'hover:text-text-primary'
                  ),
                'focus-visible:ring-2',
                'focus-visible:ring-accent-primary',
                'focus-visible:ring-offset-0'
              )}
              aria-current={active ? 'page' : undefined}
              title={item.label}
            >
              <Icon name={item.icon} />
              <span className="hidden xl:inline">{item.label}</span>
            </Link>
          )
        })}
      </nav>

      {/* Tenant Card */}
      <div className="p-4 border-t border-border-subtle bg-surface-muted" title={`${tenant.tenantName} — ${tenant.roleLabel}`}>
        <div className="flex items-center gap-3 justify-center xl:justify-start">
          <Avatar alt={tenant.tenantName} size="md" />
          <div className="hidden xl:block flex-1 min-w-0">
            <p className="text-sm font-semibold text-text-primary truncate">
              {tenant.tenantName}
            </p>
            <p className="text-xs text-text-tertiary truncate">
              {tenant.roleLabel}
            </p>
          </div>
        </div>
      </div>
    </aside>
  )
}
