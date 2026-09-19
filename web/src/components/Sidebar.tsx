import { Link, useLocation } from 'react-router-dom'
import clsx from 'clsx'

const navItems = [
  { label: 'Dashboard', path: '/', icon: 'dashboard' },
  { label: 'Inbox', path: '/inbox', icon: 'inbox' },
  { label: 'Contas', path: '/accounts', icon: 'accounts' },
  { label: 'Tickets', path: '/tickets', icon: 'tickets' },
  { label: 'Relatórios', path: '/reports', icon: 'reports' },
  { label: 'Supervisor', path: '/supervisor', icon: 'supervisor' }
]

export default function Sidebar() {
  const location = useLocation()

  const getIcon = (icon: string) => {
    switch (icon) {
      case 'dashboard':
        return <svg className="w-5 h-5" fill="currentColor" viewBox="0 0 20 20"><path d="M3 4a1 1 0 011-1h12a1 1 0 011 1v2a1 1 0 01-1 1H4a1 1 0 01-1-1V4zM3 10a1 1 0 011-1h6a1 1 0 011 1v6a1 1 0 01-1 1H4a1 1 0 01-1-1v-6zM14 9a1 1 0 00-1 1v6a1 1 0 001 1h2a1 1 0 001-1v-6a1 1 0 00-1-1h-2z"/></svg>
      case 'accounts':
        return <svg className="w-5 h-5" fill="currentColor" viewBox="0 0 20 20"><path d="M10.5 1.5H3.75A2.25 2.25 0 001.5 3.75v12.5A2.25 2.25 0 003.75 18.5h12.5a2.25 2.25 0 002.25-2.25V9.5M10 6.5a1.5 1.5 0 11-3 0 1.5 1.5 0 013 0zM7 10.5c-1 0-2 .5-2 1.5v1h5v-1c0-1-1-1.5-2-1.5h-1zm5-5h5M15 4.5v5"/></svg>
      case 'inbox':
        return <svg className="w-5 h-5" fill="currentColor" viewBox="0 0 20 20"><path d="M2 5a2 2 0 012-2h12a2 2 0 012 2v7h-4l-1 2H7l-1-2H2V5zm0 9h3l1 2h8l1-2h3v1a2 2 0 01-2 2H4a2 2 0 01-2-2v-1z"/></svg>
      case 'tickets':
        return <svg className="w-5 h-5" fill="currentColor" viewBox="0 0 20 20"><path d="M2 3a1 1 0 011-1h2.153a1 1 0 01.986.763l.296 1.485a1 1 0 00.963.807h3.204a1 1 0 00.963-.807l.296-1.485a1 1 0 01.986-.763h2.153a1 1 0 011 1v2a1 1 0 01-1 1H3a1 1 0 01-1-1V3zM2 9a1 1 0 011-1h14a1 1 0 011 1v8a2 2 0 01-2 2H4a2 2 0 01-2-2V9z"/></svg>
      case 'reports':
        return <svg className="w-5 h-5" fill="currentColor" viewBox="0 0 20 20"><path d="M3 4a1 1 0 011-1h12a1 1 0 110 2H4a1 1 0 01-1-1zm0 4a1 1 0 011-1h12a1 1 0 110 2H4a1 1 0 01-1-1zm0 4a1 1 0 011-1h12a1 1 0 110 2H4a1 1 0 01-1-1zm0 4a1 1 0 011-1h12a1 1 0 110 2H4a1 1 0 01-1-1z"/></svg>
      case 'supervisor':
        return <svg className="w-5 h-5" fill="currentColor" viewBox="0 0 20 20"><path d="M13 6a3 3 0 11-6 0 3 3 0 016 0zM18 8a2 2 0 11-4 0 2 2 0 014 0zM14 15a4 4 0 00-8 0v4h8v-4zM6 8a2 2 0 11-4 0 2 2 0 014 0zM16 18v-3a5.972 5.972 0 00-.75-2.906A3.005 3.005 0 0119 15v3h-3zM4.75 12.094A5.973 5.973 0 004 15v3H1v-3a3 3 0 013.75-2.906z"/></svg>
      default:
        return null
    }
  }

  return (
    <aside className="w-64 bg-slate-900 text-white flex flex-col">
      <nav className="flex-1 px-4 py-6 space-y-2">
        {navItems.map(item => {
          const isActive = item.path === '/' ? location.pathname === '/' : location.pathname === item.path || location.pathname.startsWith(item.path + '/')
          return (
            <Link
              key={item.path}
              to={item.path}
              className={clsx(
                'flex items-center gap-3 px-4 py-3 rounded-lg transition-colors',
                isActive
                  ? 'bg-blue-600 text-white'
                  : 'text-slate-300 hover:bg-slate-800'
              )}
            >
              {getIcon(item.icon)}
              <span>{item.label}</span>
            </Link>
          )
        })}
      </nav>
    </aside>
  )
}
