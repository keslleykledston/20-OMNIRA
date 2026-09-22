import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import clsx from 'clsx'
import { Badge, Card, EmptyState, ErrorState, Icon, PageHeader, Skeleton, Tabs, type TabItem } from '../components/primitives'
import { getTenantId } from '../lib/session'
import { teamAPI } from '../lib/team'
import { useAccess } from '../lib/useAccess'
import { ROLE_DISPLAY_NAME } from '../lib/roles'
import { PERMISSION_GROUPS, unlistedPermissions } from '../lib/permissions'

const ROLE_ORDER = ['tenant_admin', 'tenant_supervisor', 'tenant_agent']

// Somente leitura (IAM3 MVP): os papéis são fixos da plataforma. Não há criação,
// edição nem exclusão de papéis — nenhum controle editável nesta tela.
export function RolesPermissionsPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const roles = useQuery({
    queryKey: ['roles', tenantId],
    queryFn: () => teamAPI.roles(),
    retry: false,
  })
  const [selected, setSelected] = useState<string | null>(null)

  const ordered = useMemo(
    () =>
      [...(roles.data ?? [])].sort(
        (a, b) => ROLE_ORDER.indexOf(a.key) - ROLE_ORDER.indexOf(b.key),
      ),
    [roles.data],
  )
  const currentKey = access.data?.role_key
  const activeKey = selected ?? (ordered.find((r) => r.key === currentKey) ?? ordered[0])?.key
  const active = ordered.find((r) => r.key === activeKey)

  const tabs: TabItem[] = ordered.map((r) => ({
    id: r.key,
    label: ROLE_DISPLAY_NAME[r.key] ?? r.name,
  }))

  const header = (
    <PageHeader
      title="Funções e permissões"
      breadcrumbs={[{ label: 'Equipe e acesso', href: '/settings/team' }, { label: 'Funções e permissões', current: true }]}
    />
  )

  if (roles.isLoading) {
    return (
      <div className="space-y-6">
        {header}
        <Skeleton count={4} height="h-10" />
      </div>
    )
  }

  if (!roles.isError && ordered.length === 0) {
    return (
      <div className="space-y-6">
        {header}
        <EmptyState title="Nenhuma função disponível" description="Não há funções configuradas nesta organização ainda." />
      </div>
    )
  }

  if (roles.isError || !active) {
    const forbidden = (roles.error as { response?: { status?: number } } | null)?.response?.status === 403
    return (
      <div className="space-y-6">
        {header}
        <ErrorState
          title={forbidden ? 'Sem acesso' : 'Erro ao carregar'}
          message={
            forbidden
              ? 'Você não tem permissão para ver as funções e permissões.'
              : 'Não foi possível carregar as funções e permissões.'
          }
        />
      </div>
    )
  }

  const granted = new Set(active.permissions)
  const others = unlistedPermissions(active.permissions)

  return (
    <div className="space-y-6">
      {header}

      <div className="space-y-3">
        <Tabs aria-label="Funções" items={tabs} value={active.key} onChange={setSelected} />
        <div className="flex flex-wrap items-center gap-2 text-body-sm text-text-secondary">
          <p>As permissões deste papel são definidas pela plataforma.</p>
          {active.key === currentKey && <Badge variant="info" size="sm">Seu papel</Badge>}
        </div>
      </div>

      <div
        role="tabpanel"
        tabIndex={0}
        aria-label={`Permissões: ${ROLE_DISPLAY_NAME[active.key] ?? active.name}`}
        className="space-y-4 rounded-card outline-none focus-visible:ring-2 focus-visible:ring-accent-primary"
      >
        {PERMISSION_GROUPS.map((group) => (
          <MatrixSection
            key={group.title}
            title={group.title}
            rows={group.items.map((i) => ({ key: i.key, label: i.label, allowed: granted.has(i.key) }))}
          />
        ))}
        {others.length > 0 && (
          <MatrixSection title="Outras" rows={others.map((k) => ({ key: k, label: k, allowed: true }))} />
        )}
      </div>

      <p className="text-body-sm text-text-tertiary">
        <Link to="/settings/team" className="text-accent-primary hover:underline">← Equipe e acesso</Link>
      </p>
    </div>
  )
}

function MatrixSection({ title, rows }: { title: string; rows: { key: string; label: string; allowed: boolean }[] }) {
  return (
    <Card>
      <section aria-labelledby={`perm-${title}`}>
        <h2 id={`perm-${title}`} className="px-4 pb-1 pt-3 text-[10px] font-bold uppercase tracking-wide text-text-tertiary">{title}</h2>
        <ul className="divide-y divide-border-subtle border-t border-border-subtle">
          {rows.map((row) => (
            <li key={row.key} className="flex min-w-0 items-center justify-between gap-4 px-4 py-2.5 text-body-sm">
              <span className="text-text-primary">{row.label}</span>
              <span
                className={clsx(
                  'inline-flex items-center gap-1 text-xs font-medium',
                  row.allowed ? 'text-status-success' : 'text-text-tertiary',
                )}
              >
                <Icon name={row.allowed ? 'check' : 'info'} className="h-4 w-4" aria-hidden="true" />
                {row.allowed ? 'Permitido' : 'Não permitido'}
              </span>
            </li>
          ))}
        </ul>
      </section>
    </Card>
  )
}
