import type { ReactNode } from 'react'
import { PermissionState } from '../primitives'
import { SettingsShell, SETTINGS_SECTIONS } from '../SettingsShell'
import { useAccess } from '../../lib/useAccess'

// Moldura comum das páginas de Atendimento (ADR-0044). A API decide o que cada pessoa pode; aqui só se evita oferecer o que a matriz recusaria.
export function AttendanceFrame({ title, description, actions, children }: { title: string; description: string; actions?: ReactNode; children: (can: { read: boolean; manage: boolean }) => ReactNode }) {
  const access = useAccess()
  const read = access.can('agent.read')
  const manage = access.can('agent.manage')
  const section = SETTINGS_SECTIONS.find((s) => s.key === 'attendance')
  return (
    <SettingsShell sections={SETTINGS_SECTIONS} subsections={section?.subsections} title={title} description={description} actions={actions}>
      {!access.isLoading && !read ? <PermissionState message="Você não tem permissão para ver a configuração de atendimento." /> : children({ read, manage })}
    </SettingsShell>
  )
}

export function Notice({ tone = 'info', children }: { tone?: 'info' | 'warning'; children: ReactNode }) {
  return <p className={`rounded-control p-3 text-sm text-text-secondary ${tone === 'warning' ? 'bg-status-warning-soft' : 'bg-status-info-soft'}`}>{children}</p>
}

export const selectClass = 'w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary'

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs font-medium text-text-secondary">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-xs text-text-tertiary">{hint}</span>}
    </label>
  )
}
