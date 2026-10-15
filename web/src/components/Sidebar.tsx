import { useState, type ElementType } from 'react'
import { BrandMark } from './BrandMark'
import { Link, useLocation } from 'react-router-dom'
import clsx from 'clsx'
import { Avatar, Icon } from './primitives'
import type { IconName } from './primitives/Icon'
import { useTenantDisplay } from '../lib/tenantContext'
import { isDevSurface } from './UnavailableSurface'
import { isActing } from '../lib/acting'
import { useMyHubs } from '../hooks/useMyHubs'
import { useHubWaiting } from '../hooks/useHubWaiting'

// Only routes that exist in App.tsx. Spec items without a route (Automação, o
// restante de Configurações) stay hidden rather than simulated —
// docs/architecture/FRONTEND-UX.md. Equipe e acesso é a única seção de
// Configurações implementada até agora (IAM2A).
//
// mockBacked: destination has no real backend behind it (Relatórios —
// DESIGN.5 reality gate, FRONTEND.1). Filtered out of operational
// navigation outside development so it never implies active production
// capability; the route itself still exists (see UnavailableSurface).
// Supervisor became real in PRODUCT.1, Tickets in PRODUCT.2-B, Dashboard in
// PRODUCT.3-B — none of the three is contained anymore.
const navItems: { label: string; path: string; icon: IconName; alsoActiveOn?: string; mockBacked?: boolean; external?: boolean }[] = [
  { label: 'Dashboard', path: '/', icon: 'dashboard' },
  { label: 'Conversas', path: '/inbox', icon: 'conversations' },
  { label: 'Tickets', path: '/tickets', icon: 'tickets' },
  // PRODUCT.7A2: always visible in the sidebar, same convention as
  // Supervisor — access itself is gated inside the page
  // (useAccess().can('ticket.reconcile') + PermissionState), never by
  // hiding the nav entry.
  { label: 'Reconciliação', path: '/ticket-reconciliation', icon: 'clock' },
  // ADR-0015: always visible, like Supervisor; access is gated inside the page (group.read).
  { label: 'Grupos', path: '/groups', icon: 'whatsapp' },
  // ADR-0019: always visible like Supervisor; each area is gated inside the page by flow.view / flow_template.view / flow_run.view.
  { label: 'Automação', path: '/flows', icon: 'sparkles' },
  { label: 'Contatos', path: '/contacts', icon: 'contacts' },
  { label: 'Canais', path: '/channels', icon: 'channels' },
  { label: 'Relatórios', path: '/reports', icon: 'reports', mockBacked: true },
  { label: 'Supervisor', path: '/supervisor', icon: 'supervisor' },
  { label: 'Pessoas', path: '/settings/people', icon: 'contacts', alsoActiveOn: '/settings/roles' },
  { label: 'Atendimento', path: '/settings/attendance', icon: 'clock', alsoActiveOn: '/settings/attendance/sectors,/settings/attendance/calendars,/settings/attendance/shifts,/settings/attendance/overflow' },
  { label: 'Configurações', path: '/settings/general', icon: 'settings' },
]

// The menu can be folded to icons on wide screens too (below xl it is always icons): the choice is a per-viewer convenience, remembered locally.
const COLLAPSED_KEY = 'omnira.sidebar.collapsed'
function readCollapsed(): boolean {
  try {
    return localStorage.getItem(COLLAPSED_KEY) === '1'
  } catch {
    return false
  }
}

export default function Sidebar() {
  const location = useLocation()
  const [collapsed, setCollapsed] = useState(readCollapsed)
  const toggleCollapsed = () => {
    const next = !collapsed
    setCollapsed(next)
    try {
      localStorage.setItem(COLLAPSED_KEY, next ? '1' : '0')
    } catch {
      /* the choice just is not remembered */
    }
  }
  // widths and labels: from xl up they follow `collapsed`; below xl the menu is always icons
  const wide = (cls: string) => (collapsed ? '' : cls)
  const tenant = useTenantDisplay()
  const hubs = useMyHubs()
  // Conversations waiting for an answer in the instances served through the Hub: shown on "Conversas" (display only; not while attending one instance)
  const waiting = useHubWaiting(hubs.data ?? [], !isActing())
  const base = navItems.filter((item) => !item.mockBacked || isDevSurface())
  // ADR-0041: administration (Hub access, instances and their channels) lives in the console (/admin/, another entry of the build). The agent menu only
  // points to it, for the people the server says may use it (display only; the console and the API check again).
  const consoleItem: (typeof navItems)[number] = { label: 'Administração', path: '/admin/', icon: 'key', external: true }
  const canAdmin = (hubs.data ?? []).some((h) => h.can_manage_access || h.can_manage_instances)
  // There is no separate "Hub" entry: "Conversas" is the one inbox, and it widens to every authorized company by itself (ADR-0039 revisão).
  const allItems = canAdmin ? [...base, consoleItem] : base
  // Attending an instance through the Hub (ADR-0040): only the conversations are available there for now, nothing else of the instance is offered.
  const visibleNavItems = isActing() ? allItems.filter((i) => i.path === '/inbox') : allItems

  const isActive = (path: string) =>
    path === '/' ? location.pathname === '/' : location.pathname === path || location.pathname.startsWith(path + '/')

  return (
    <aside
      className={clsx(
        // Compact (icon-only) on tablet, full width from xl up — 01-FOUNDATION/APP-SHELL.md
        'w-sidebar-compact',
        wide('xl:w-sidebar'),
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
      <div className={clsx('px-4 py-6 border-b border-border-subtle', wide('xl:px-6'))}>
        <div className={clsx('flex items-center gap-2 justify-center', wide('xl:justify-start'), collapsed && 'xl:flex-col xl:gap-3')}>
          <BrandMark size={32} label="OMNIRA" />
          <span className={clsx('hidden font-bold text-text-primary text-lg', wide('xl:inline'))}>OMNIRA</span>
          <button
            type="button"
            onClick={toggleCollapsed}
            aria-label={collapsed ? 'Expandir o menu' : 'Recolher o menu'}
            aria-expanded={!collapsed}
            title={collapsed ? 'Expandir o menu' : 'Recolher o menu'}
            className={clsx(
              'hidden xl:flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-control text-text-tertiary hover:bg-surface-muted hover:text-text-primary',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary',
              collapsed ? '' : 'ml-auto',
            )}
          >
            <Icon name="chevron-down" size={16} className={collapsed ? '-rotate-90' : 'rotate-90'} />
          </button>
        </div>
      </div>

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto px-3 py-4 space-y-1">
        {visibleNavItems.map(item => {
          const alsoActivePaths = item.alsoActiveOn?.split(',').map(p => p.trim()) || []
          const active = isActive(item.path) || alsoActivePaths.some(p => isActive(p))
          // `external`: another entry of the build (the console), so a plain link with a full page load, not the router's.
          const NavTag: ElementType = item.external ? 'a' : Link
          return (
            <NavTag
              key={item.path}
              {...(item.external ? { href: item.path } : { to: item.path })}
              className={clsx(
                'flex',
                'items-center',
                'gap-3',
                'justify-center',
                wide('xl:justify-start'),
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
              aria-label={item.path === '/inbox' && waiting > 0 ? `${item.label}, ${waiting} aguardando resposta` : undefined}
              title={item.path === '/inbox' && waiting > 0 ? `${item.label} — ${waiting} aguardando resposta` : item.label}
            >
              <span className="relative inline-flex">
                <Icon name={item.icon} />
                {item.path === '/inbox' && waiting > 0 && (
                  <span
                    aria-hidden="true"
                    className={clsx('absolute -right-2 -top-2 min-w-[16px] rounded-full bg-accent-primary px-1 text-center text-[10px] font-semibold leading-4 text-white', wide('xl:hidden'))}
                  >
                    {waiting > 99 ? '99+' : waiting}
                  </span>
                )}
              </span>
              <span className={clsx('hidden', wide('xl:inline'))}>{item.label}</span>
              {item.path === '/inbox' && waiting > 0 && (
                <span aria-hidden="true" className={clsx('ml-auto hidden rounded-full bg-accent-primary px-1.5 text-[11px] font-semibold leading-5 text-white', wide('xl:inline'))}>
                  {waiting > 99 ? '99+' : waiting}
                </span>
              )}
            </NavTag>
          )
        })}
      </nav>

      {/* Tenant Card */}
      <div className="p-4 border-t border-border-subtle bg-surface-muted" title={`${tenant.tenantName} — ${tenant.roleLabel}`}>
        <div className={clsx('flex items-center gap-3 justify-center', wide('xl:justify-start'))}>
          <Avatar alt={tenant.tenantName} size="md" />
          <div className={clsx('hidden flex-1 min-w-0', wide('xl:block'))}>
            <p className="text-sm font-semibold text-text-primary truncate">
              {tenant.tenantName}
            </p>
            <p className="text-xs text-text-tertiary truncate">
              {tenant.roleLabel}
            </p>
            <p className="text-[10px] text-text-quaternary truncate font-mono">
              {tenant.tenantId}
            </p>
          </div>
        </div>
      </div>
    </aside>
  )
}
