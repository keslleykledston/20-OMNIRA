import { useState, type ElementType } from 'react'
import { BrandMark } from './BrandMark'
import { Link, useLocation } from 'react-router-dom'
import clsx from 'clsx'
import { Avatar, Icon } from './primitives'
import { navItems, GROUP_ORDER, GROUP_TITLE, type NavItem } from '../lib/navItems'
import { useTenantDisplay } from '../lib/tenantContext'
import { isDevSurface } from './UnavailableSurface'
import { isActing } from '../lib/acting'
import { useMyHubs } from '../hooks/useMyHubs'
import { useHubWaiting } from '../hooks/useHubWaiting'

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
  const consoleItem: NavItem = { label: 'Administração', path: '/admin/', icon: 'shield-check', group: 'configuracao', external: true }
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
      <nav className="flex-1 overflow-y-auto px-3 py-4">
        {GROUP_ORDER.map((group, gi) => {
          const items = visibleNavItems.filter((i) => i.group === group)
          if (items.length === 0) return null
          return (
            <div key={group} role="group" aria-label={GROUP_TITLE[group]} className={clsx('space-y-1', gi > 0 && 'mt-4')}>
              {/* Title only when the menu is wide; folded to icons, a thin line separates the groups. */}
              <p className={clsx('hidden px-3 pb-1 text-[11px] font-semibold uppercase tracking-wider text-text-tertiary', wide('xl:block'))}>{GROUP_TITLE[group]}</p>
              <div aria-hidden="true" className={clsx('mx-3 mb-2 h-px bg-border-subtle', gi === 0 && 'hidden', wide('xl:hidden'))} />
              {items.map(item => {
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
                          className={clsx('absolute -right-2 -top-2 min-w-[16px] rounded-full bg-accent-primary px-1 text-center text-[11px] font-semibold leading-4 text-white', wide('xl:hidden'))}
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
            </div>
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
            <p className="text-[11px] text-text-quaternary truncate font-mono">
              {tenant.tenantId}
            </p>
          </div>
        </div>
      </div>
    </aside>
  )
}
