import { Link, useLocation } from 'react-router-dom'
import clsx from 'clsx'
import { PageHeader } from './primitives'

export interface SettingsSection {
  key: string
  label: string
  /** Real route (e.g. /settings/team) — each section is its own page, not an in-shell tab. */
  href: string
  /** Optional subsections (tabs within this section) */
  subsections?: SettingsSection[]
}

// Admin/settings IA: only routes that exist today in App.tsx. "Pessoas" is who works here (one page with a sheet per person);
// "Atendimento" is how conversations flow (queues, sectors, hours, shifts, overflow).
export const SETTINGS_SECTIONS: SettingsSection[] = [
  {
    key: 'people',
    label: 'Pessoas',
    href: '/settings/people',
    subsections: [
      { key: 'people', label: 'Equipe', href: '/settings/people' },
      { key: 'roles', label: 'Funções e permissões', href: '/settings/roles' },
    ],
  },
  {
    // ADR-0044: setores, horários, escalas, plantões, transbordo e SLA. Cada subseção é uma página própria, como em Pessoas.
    key: 'attendance',
    label: 'Setores e horários',
    href: '/settings/attendance',
    subsections: [
      { key: 'sectors', label: 'Filas e setores', href: '/settings/attendance/sectors' },
      { key: 'calendars', label: 'Horários', href: '/settings/attendance/calendars' },
      { key: 'shifts', label: 'Escalas e plantões', href: '/settings/attendance/shifts' },
      { key: 'overflow', label: 'Transbordo e SLA', href: '/settings/attendance/overflow' },
    ],
  },
  {
    // ADR-0048: aplicações externas (CRMs) que usam a API headless. A rota responde "não habilitado" quando a plataforma não liga o recurso.
    key: 'integrations',
    label: 'Integrações',
    href: '/settings/integrations',
    subsections: [{ key: 'apps', label: 'Aplicações externas', href: '/settings/integrations/apps' }],
  },
  { key: 'accounts', label: 'Empresas', href: '/accounts' },
]

interface SettingsShellProps {
  sections: SettingsSection[]
  title: string
  description?: string
  actions?: React.ReactNode
  children: React.ReactNode
  /** Optional subsection tabs (shown below main title) */
  subsections?: SettingsSection[]
}

/** Layout foundation for admin/settings pages: local section nav + title/description/actions
 *  + content region. No business data lives here — sections navigate to real routes via
 *  react-router-dom, same pattern as the global Sidebar. Below `md` the nav collapses to a
 *  horizontal scrollable row above the content (smallest interaction that stays usable at
 *  390px without a drawer); from `md` up it sits as a left column beside the content. */
export function SettingsShell({ sections, subsections, title, description, actions, children }: SettingsShellProps) {
  const location = useLocation()
  const isActive = (href: string) => location.pathname === href || location.pathname.startsWith(href + '/')

  return (
    <div className="md:grid md:grid-cols-[200px_minmax(0,1fr)] md:items-start md:gap-6 md:px-6 md:py-6">
      <nav aria-label="Configurações" className="border-b border-border-subtle px-4 py-3 md:border-b-0 md:px-0 md:py-0">
        <div className="flex gap-1 overflow-x-auto md:flex-col">
          {sections.map((section) => {
            const active = isActive(section.href) || (section.subsections?.some(sub => isActive(sub.href)) ?? false)
            return (
              <Link
                key={section.key}
                to={section.href}
                aria-current={active ? 'page' : undefined}
                className={clsx(
                  'shrink-0',
                  'whitespace-nowrap',
                  'rounded-control',
                  'flex',
                  'items-center',
                  'min-h-[44px]',
                  'px-3',
                  'py-2',
                  'text-sm',
                  'font-medium',
                  'no-underline',
                  'transition-colors',
                  'focus-visible:ring-2',
                  'focus-visible:ring-accent-primary',
                  active
                    ? clsx('bg-accent-primary-soft', 'text-accent-primary')
                    : clsx('text-text-secondary', 'hover:bg-surface-muted', 'hover:text-text-primary')
                )}
              >
                {section.label}
              </Link>
            )
          })}
        </div>
      </nav>

      <div className="min-w-0">
        <PageHeader title={title} description={description} actions={actions} className="md:border-b-0 md:px-0 md:py-0 md:bg-transparent" />

        {subsections && subsections.length > 0 && (
          <nav className="mt-4 border-b border-border-subtle flex gap-1 overflow-x-auto px-4 md:px-0 md:-mx-0">
            {subsections.map((section) => {
              const active = isActive(section.href)
              return (
                <Link
                  key={section.key}
                  to={section.href}
                  aria-current={active ? 'page' : undefined}
                  className={clsx(
                    'shrink-0',
                    'whitespace-nowrap',
                    'px-3',
                    'py-2',
                    'text-sm',
                    'font-medium',
                    'no-underline',
                    'transition-colors',
                    'border-b-2',
                    active
                      ? clsx('border-accent-primary', 'text-accent-primary')
                      : clsx('border-transparent', 'text-text-secondary', 'hover:text-text-primary')
                  )}
                >
                  {section.label}
                </Link>
              )
            })}
          </nav>
        )}

        <div className="mt-6 px-4 pb-6 md:px-0 md:pb-0">{children}</div>
      </div>
    </div>
  )
}
