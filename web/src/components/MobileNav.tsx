import { useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import clsx from 'clsx'
import { Drawer, Icon } from './primitives'
import type { IconName } from './primitives/Icon'
import { useMyHubs } from '../hooks/useMyHubs'
import { isActing } from '../lib/acting'
import { navItems, GROUP_ORDER, GROUP_TITLE } from '../lib/navItems'
import { isDevSurface } from './UnavailableSurface'

// Direct bottom-bar shortcuts (FRONTEND.2 / FRONTEND-UX.md: "navegação inferior só para as funções
// essenciais"). Everything else the sidebar offers lives behind "Mais" — a drawer, not a dead end, so
// the rest of the app (Grupos, Tickets, Supervisor, Automação, Configurações…) stays reachable on a phone.
const items: { label: string; path: string; icon: IconName }[] = [
  { label: 'Conversas', path: '/inbox', icon: 'conversations' },
  { label: 'Contatos', path: '/contacts', icon: 'contacts' },
  { label: 'Canais', path: '/channels', icon: 'channels' },
]
const PINNED_PATHS = items.map((i) => i.path)

export default function MobileNav() {
  const location = useLocation()
  const hubs = useMyHubs()
  const [moreOpen, setMoreOpen] = useState(false)
  // Same rule as the desktop sidebar: no separate Hub entry; people who administer a Hub (or its instances' channels) get one link to the console (ADR-0041)
  const shown = isActing() ? items.filter((i) => i.path === '/inbox') : [...items]
  const showConsole = !isActing() && (hubs.data ?? []).some((h) => h.can_manage_access || h.can_manage_instances)
  // Attending an instance through the Hub (ADR-0040): only the conversations are available there for now, same restriction as the desktop sidebar.
  const more = isActing() ? [] : navItems.filter((i) => !PINNED_PATHS.includes(i.path) && (!i.mockBacked || isDevSurface()))
  // startsWith keeps child routes (e.g. /contacts/:id, /channels/whatsapp/new)
  // active under their parent item.
  const isActive = (path: string) => location.pathname.startsWith(path)

  const slot = 'flex flex-col items-center justify-center gap-1 flex-1 min-h-[44px] text-xs no-underline'

  return (
    <>
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
        {(more.length > 0 || showConsole) && (
          <button
            type="button"
            onClick={() => setMoreOpen(true)}
            aria-haspopup="dialog"
            aria-expanded={moreOpen}
            className={clsx(slot, 'text-text-secondary')}
          >
            <Icon name="more" size={22} />
            <span>Mais</span>
          </button>
        )}
      </nav>

      <Drawer open={moreOpen} title="Mais" onClose={() => setMoreOpen(false)}>
        <div className="px-4 py-4">
          <h2 className="mb-3 text-base font-semibold text-text-primary">Mais</h2>
          {GROUP_ORDER.map((group) => {
            const groupItems = more.filter((i) => i.group === group)
            if (groupItems.length === 0) return null
            return (
              <div key={group} className="mb-4">
                <p className="px-2 pb-1 text-[11px] font-semibold uppercase tracking-wider text-text-tertiary">{GROUP_TITLE[group]}</p>
                {groupItems.map((item) => (
                  <Link
                    key={item.path}
                    to={item.path}
                    onClick={() => setMoreOpen(false)}
                    className="flex items-center gap-3 rounded-control px-2 py-2.5 text-sm text-text-primary no-underline hover:bg-surface-muted"
                  >
                    <Icon name={item.icon} size={20} />
                    {item.label}
                  </Link>
                ))}
              </div>
            )
          })}
          {showConsole && (
            <div className="mb-4">
              <p className="px-2 pb-1 text-[11px] font-semibold uppercase tracking-wider text-text-tertiary">Administração</p>
              <a
                href="/admin/"
                className="flex items-center gap-3 rounded-control px-2 py-2.5 text-sm text-text-primary no-underline hover:bg-surface-muted"
              >
                <Icon name="shield-check" size={20} />
                Administração
              </a>
            </div>
          )}
        </div>
      </Drawer>
    </>
  )
}
