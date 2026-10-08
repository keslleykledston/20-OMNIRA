import { Link, useLocation } from 'react-router-dom'
import clsx from 'clsx'
import { Icon } from './primitives'
import type { IconName } from './primitives/Icon'
import { useMyHubs } from '../hooks/useMyHubs'

// Real operational surfaces only (FRONTEND.2). Dashboard/Tickets were mock-
// backed and are gone from here entirely, in dev too — mocks stay reachable
// directly via their existing routes, no special-casing needed on mobile nav.
const items: { label: string; path: string; icon: IconName }[] = [
  { label: 'Conversas', path: '/inbox', icon: 'conversations' },
  { label: 'Contatos', path: '/contacts', icon: 'contacts' },
  { label: 'Canais', path: '/channels', icon: 'channels' },
]

export default function MobileNav() {
  const location = useLocation()
  const hubs = useMyHubs()
  // Same rule as the desktop sidebar: the Hub entry exists only for members of a Service Hub (and only when the server has it on).
  const shown = [...((hubs.data?.length ?? 0) > 0 ? [items[0], { label: 'Hub', path: '/hub', icon: 'channels' as IconName }, ...items.slice(1)] : items)]
  // admins of a Hub also get the Access panel (ADR-0039)
  if ((hubs.data ?? []).some((h) => h.can_manage_access)) shown.splice(2, 0, { label: 'Acessos', path: '/acessos', icon: 'contacts' as IconName })
  // startsWith keeps child routes (e.g. /contacts/:id, /channels/whatsapp/new)
  // active under their parent item.
  const isActive = (path: string) => location.pathname.startsWith(path)

  const slot = 'flex flex-col items-center justify-center gap-1 flex-1 min-h-[44px] text-xs no-underline'

  return (
    <nav
      aria-label="Navegação principal"
      className="lg:hidden fixed bottom-0 inset-x-0 h-16 bg-surface border-t border-border-subtle flex items-stretch px-2"
    >
      {shown.map((item) => {
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
    </nav>
  )
}
