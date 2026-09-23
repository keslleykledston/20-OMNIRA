import { Link, useLocation } from 'react-router-dom'
import clsx from 'clsx'
import { Icon } from './primitives'
import type { IconName } from './primitives/Icon'
import { isDevSurface } from './UnavailableSurface'

// mockBacked: see Sidebar.tsx — same DESIGN.5/FRONTEND.1 rationale.
const items: { label: string; path: string; icon: IconName; mockBacked?: boolean }[] = [
  { label: 'Dashboard', path: '/', icon: 'dashboard', mockBacked: true },
  { label: 'Conversas', path: '/inbox', icon: 'conversations' },
  { label: 'Tickets', path: '/tickets', icon: 'tickets', mockBacked: true },
]

export default function MobileNav() {
  const location = useLocation()
  const isActive = (path: string) =>
    path === '/' ? location.pathname === '/' : location.pathname.startsWith(path)
  const visibleItems = items.filter((item) => !item.mockBacked || isDevSurface())

  const slot = 'flex flex-col items-center justify-center gap-1 flex-1 min-h-[44px] text-xs no-underline'

  return (
    <nav
      aria-label="Navegação principal"
      className="lg:hidden fixed bottom-0 inset-x-0 h-16 bg-surface border-t border-border-subtle flex items-stretch px-2"
    >
      {visibleItems.map((item) => {
        const active = isActive(item.path)
        return (
          <Link
            key={item.path}
            to={item.path}
            aria-current={active ? 'page' : undefined}
            className={clsx(slot, active ? 'text-accent-primary font-semibold' : 'text-text-secondary')}
          >
            <Icon name={item.icon} size={22} />
            <span>{item.label}</span>
          </Link>
        )
      })}
      {/* "Mais" menu is deferred to FR10; rendered inert instead of linking nowhere. */}
      <button
        type="button"
        aria-disabled="true"
        title="Disponível em breve"
        className={clsx(slot, 'text-text-tertiary')}
      >
        <Icon name="more" size={22} />
        <span>Mais</span>
      </button>
    </nav>
  )
}
