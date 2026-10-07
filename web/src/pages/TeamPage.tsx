import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Avatar,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DropdownMenu,
  EmptyState,
  ErrorState,
  FilterBar,
  Icon,
  Input,
  Modal,
  Skeleton,
  StatusBadge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  Tabs,
  type IconName,
  type MenuAction,
  type TabItem,
} from '../components/primitives'
import { SettingsShell, SETTINGS_SECTIONS } from '../components/SettingsShell'
import { getTenantId } from '../lib/session'
import { teamAPI, teamErrorMessage, type MembershipStatus, type RoleOption, type TeamMember } from '../lib/team'
import { invitationsAPI, invitationErrorMessage, type Invitation, type InvitationStatus } from '../lib/invitations'
import { ROLE_DISPLAY_NAME } from '../lib/roles'
import { useAccess } from '../lib/useAccess'

const ROLE_BADGE: Record<string, { label: string; variant: 'info' | 'default' | 'warning' }> = {
  tenant_admin: { label: 'Admin', variant: 'info' },
  tenant_supervisor: { label: 'Supervisor', variant: 'default' },
  tenant_agent: { label: 'Agente', variant: 'warning' },
}

const STATUS_BADGE: Record<MembershipStatus, { label: string; tone: 'success' | 'default' | 'danger' }> = {
  active: { label: 'Ativo', tone: 'success' },
  inactive: { label: 'Inativo', tone: 'default' },
  revoked: { label: 'Revogado', tone: 'danger' },
}

const INVITATION_STATUS_BADGE: Record<InvitationStatus, { label: string; tone: 'success' | 'default' | 'danger' | 'warning' }> = {
  pending: { label: 'Pendente', tone: 'warning' },
  accepted: { label: 'Aceito', tone: 'success' },
  revoked: { label: 'Revogado', tone: 'danger' },
  expired: { label: 'Expirado', tone: 'default' },
}

type TeamTab = 'users' | 'invitations'
const TEAM_TABS: TabItem<TeamTab>[] = [
  { id: 'users', label: 'Usuários' },
  { id: 'invitations', label: 'Convites' },
]

function formatExpiresIn(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const diffMs = d.getTime() - Date.now()
  if (diffMs <= 0) return 'Expirado'
  const hours = Math.round(diffMs / (1000 * 60 * 60))
  if (hours < 24) return `Em ${hours}h`
  return `Em ${Math.round(hours / 24)}d`
}

function formatDateTime(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString('pt-BR', { dateStyle: 'short', timeStyle: 'short' })
}

function initials(name: string, fallback: string): string {
  const source = name.trim() || fallback
  const parts = source.split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

function formatLastLogin(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const now = new Date()
  const sameDay = d.toDateString() === now.toDateString()
  const time = d.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' })
  if (sameDay) return `Hoje, ${time}`
  return d.toLocaleDateString('pt-BR', { day: '2-digit', month: 'short', year: 'numeric' }) + ', ' + time
}

export default function TeamPage() {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()

  const [tab, setTab] = useState<TeamTab>('users')
  const [search, setSearch] = useState('')
  const [roleFilter, setRoleFilter] = useState<string>('all')
  const [statusFilter, setStatusFilter] = useState<string>('all')
  const [roleEditTarget, setRoleEditTarget] = useState<TeamMember | null>(null)
  const [pendingStatus, setPendingStatus] = useState<{ member: TeamMember; status: MembershipStatus } | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [pendingRevokeInvite, setPendingRevokeInvite] = useState<Invitation | null>(null)

  const access = useAccess()
  const canManage = access.can('membership.manage')
  const deliveryAvailable = access.data?.invitation_delivery_available ?? false
  const canInvite = canManage && deliveryAvailable

  const team = useQuery({
    queryKey: ['team', tenantId],
    queryFn: () => teamAPI.list(),
    retry: false,
    enabled: !access.isError,
  })

  const roles = useQuery({
    queryKey: ['team-roles', tenantId],
    queryFn: () => teamAPI.roles(),
    retry: false,
    enabled: canManage,
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['team', tenantId] })

  const changeRole = useMutation({
    mutationFn: ({ membershipId, roleKey }: { membershipId: string; roleKey: string }) =>
      teamAPI.updateRole(membershipId, roleKey),
    onSuccess: () => {
      setRoleEditTarget(null)
      void invalidate()
    },
    onError: (e) => setActionError(teamErrorMessage(e, 'Não foi possível alterar a função.')),
  })

  const changeStatus = useMutation({
    mutationFn: ({ membershipId, status }: { membershipId: string; status: MembershipStatus }) =>
      teamAPI.updateStatus(membershipId, status),
    onSuccess: () => {
      setPendingStatus(null)
      void invalidate()
    },
    onError: (e) => setActionError(teamErrorMessage(e, 'Não foi possível concluir a operação.')),
  })

  const invitations = useQuery({
    queryKey: ['team-invitations', tenantId],
    queryFn: () => invitationsAPI.list(),
    retry: false,
    enabled: !access.isError,
  })
  const invalidateInvitations = () => queryClient.invalidateQueries({ queryKey: ['team-invitations', tenantId] })

  const createInvitation = useMutation({
    mutationFn: ({ email, roleKey }: { email: string; roleKey: string }) => invitationsAPI.create(email, roleKey),
    onSuccess: (inv) => {
      setInviteOpen(false)
      setActionError(null)
      setNotice(inv.sent_at ? `Convite enviado para ${inv.email}.` : `Convite criado para ${inv.email}. Copie o link em "Convites".`)
      void invalidateInvitations()
    },
    onError: (e) => {
      setInviteOpen(false)
      setNotice(null)
      setActionError(invitationErrorMessage(e, 'Não foi possível enviar o convite.'))
      // 502: o convite foi registrado sem entrega; a lista precisa mostrá-lo para reenviar.
      void invalidateInvitations()
    },
  })

  const resendInvitation = useMutation({
    mutationFn: (id: string) => invitationsAPI.resend(id),
    onSuccess: (inv) => {
      setActionError(null)
      setNotice(`Convite reenviado para ${inv.email}.`)
      void invalidateInvitations()
    },
    onError: (e) => {
      setNotice(null)
      setActionError(invitationErrorMessage(e, 'Não foi possível reenviar o convite.'))
      void invalidateInvitations()
    },
  })

  const revokeInvitation = useMutation({
    mutationFn: (id: string) => invitationsAPI.revoke(id),
    onSuccess: () => {
      setPendingRevokeInvite(null)
      void invalidateInvitations()
    },
    onError: (e) => setActionError(invitationErrorMessage(e, 'Não foi possível revogar o convite.')),
  })

  const members = team.data ?? []

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return members.filter((m) => {
      if (roleFilter !== 'all' && m.role_key !== roleFilter) return false
      if (statusFilter !== 'all' && m.status !== statusFilter) return false
      if (q && !m.name.toLowerCase().includes(q) && !m.email.toLowerCase().includes(q)) return false
      return true
    })
  }, [members, search, roleFilter, statusFilter])

  const summary = useMemo(() => {
    const active = members.filter((m) => m.status === 'active')
    return {
      total: active.length,
      admins: active.filter((m) => m.role_key === 'tenant_admin').length,
      supervisors: active.filter((m) => m.role_key === 'tenant_supervisor').length,
      agents: active.filter((m) => m.role_key === 'tenant_agent').length,
    }
  }, [members])

  const peopleSection = SETTINGS_SECTIONS.find(s => s.key === 'people')
  const subsections = peopleSection?.subsections || []

  if (access.isError) {
    return (
      <SettingsShell sections={SETTINGS_SECTIONS} subsections={subsections} title="Equipe e acesso">
        <ErrorState
          title="Sem acesso"
          message="Você não tem permissão para gerenciar a equipe."
        />
      </SettingsShell>
    )
  }

  return (
    <SettingsShell
      sections={SETTINGS_SECTIONS}
      subsections={subsections}
      title="Equipe e acesso"
      description="Gerencie os usuários da sua equipe, defina permissões e controle o acesso à plataforma."
      actions={
        <div className="flex flex-wrap items-center gap-2">
        <Link
          to="/settings/roles"
          className="inline-flex items-center rounded-control border border-border-subtle bg-surface-muted px-4 py-3 text-body-sm font-medium text-text-primary hover:bg-surface-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary"
        >
          Funções e permissões
        </Link>
        {canInvite ? (
          <Button variant="primary" onClick={() => setInviteOpen(true)}>
            <Icon name="plus" size={16} />
            Convidar usuário
          </Button>
        ) : (
          <Button
            variant="primary"
            disabled
            title={
              !canManage
                ? 'Requer permissão para gerenciar a equipe'
                : 'Envio de convites ainda não está configurado neste ambiente.'
            }
          >
            <Icon name="plus" size={16} />
            Convidar usuário
          </Button>
        )}
        </div>
      }
    >
      <div className="space-y-6">
      {/* Guia Visual de Fluxo */}
      <Card className="bg-gradient-to-r from-accent-primary-soft to-accent-primary/5 border-accent-primary/20">
        <div className="p-6">
          <h3 className="font-semibold text-text-primary mb-4">Como adicionar e gerenciar agentes</h3>
          <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
            {[
              { num: 1, title: 'Convidar', desc: 'Envie convite por email' },
              { num: 2, title: 'Aceitar', desc: 'Membro aceita + muda senha' },
              { num: 3, title: 'Ativar Agente', desc: 'Admin ativa em "Agentes"' },
              { num: 4, title: 'Adicionar Filas', desc: 'Associe às filas de atendimento' },
            ].map((step, i) => (
              <div key={i} className="flex flex-col items-center text-center">
                <div className="w-10 h-10 rounded-full bg-accent-primary text-white flex items-center justify-center font-bold mb-2">
                  {step.num}
                </div>
                <p className="font-medium text-sm text-text-primary">{step.title}</p>
                <p className="text-xs text-text-secondary mt-1">{step.desc}</p>
              </div>
            ))}
          </div>
        </div>
      </Card>

      {notice && (
        <div
          role="status"
          className="flex items-center justify-between gap-3 rounded-card border border-status-success-border bg-status-success-soft px-4 py-3 text-body-sm text-status-success"
        >
          <span>{notice}</span>
          <button type="button" className="underline" onClick={() => setNotice(null)}>Fechar</button>
        </div>
      )}
      {actionError && (
        <ErrorState message={actionError} isDismissible onDismiss={() => setActionError(null)} />
      )}

      <Tabs items={TEAM_TABS} value={tab} onChange={setTab} aria-label="Seções de Equipe e acesso" />

      {tab === 'users' && (
        <>
      {/* Informações sobre Funções */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <Card className="border-l-4 border-l-accent-primary">
          <div className="p-4">
            <h4 className="font-semibold text-text-primary flex items-center gap-2">
              <Icon name="settings" size={18} className="text-accent-primary" />
              Admin
            </h4>
            <p className="text-xs text-text-secondary mt-2">Acesso total: convida usuários, gerencia permissões, ativa agentes, adiciona filas.</p>
          </div>
        </Card>
        <Card className="border-l-4 border-l-accent-secondary">
          <div className="p-4">
            <h4 className="font-semibold text-text-primary flex items-center gap-2">
              <Icon name="supervisor" size={18} className="text-accent-secondary" />
              Supervisor
            </h4>
            <p className="text-xs text-text-secondary mt-2">Gerencia agentes: ativa, desativa, adiciona/remove de filas, monitora presença.</p>
          </div>
        </Card>
        <Card className="border-l-4 border-l-accent-tertiary">
          <div className="p-4">
            <h4 className="font-semibold text-text-primary flex items-center gap-2">
              <Icon name="conversations" size={18} className="text-accent-tertiary" />
              Agente
            </h4>
            <p className="text-xs text-text-secondary mt-2">Operacional: recebe atendimentos, responde conversas, gerencia sua presença.</p>
          </div>
        </Card>
      </div>

      {!team.isLoading && !team.isError && (
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <SummaryCard icon="contacts" label="Usuários ativos" value={summary.total} />
          <SummaryCard icon="supervisor" label="Admins" value={summary.admins} />
          <SummaryCard icon="conversations" label="Supervisores" value={summary.supervisors} />
          <SummaryCard icon="dashboard" label="Agentes" value={summary.agents} />
        </div>
      )}

      <FilterBar>
        <div className="flex-1">
          <Input
            placeholder="Buscar por nome ou e-mail…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <select
          aria-label="Filtrar por função"
          value={roleFilter}
          onChange={(e) => setRoleFilter(e.target.value)}
          className="h-10 rounded-control border border-border-light bg-surface px-3 text-sm text-text-primary"
        >
          <option value="all">Todas as funções</option>
          <option value="tenant_admin">Admin</option>
          <option value="tenant_supervisor">Supervisor</option>
          <option value="tenant_agent">Agente</option>
        </select>
        <select
          aria-label="Filtrar por status"
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          className="h-10 rounded-control border border-border-light bg-surface px-3 text-sm text-text-primary"
        >
          <option value="all">Todos os status</option>
          <option value="active">Ativo</option>
          <option value="inactive">Inativo</option>
          <option value="revoked">Revogado</option>
        </select>
      </FilterBar>

      {team.isLoading && <TeamSkeleton />}

      {team.isError && (
        <ErrorState
          message={teamErrorMessage(team.error, 'Não foi possível carregar a equipe.')}
          action={{ label: 'Tentar novamente', onClick: () => void team.refetch() }}
        />
      )}

      {!team.isLoading && !team.isError && filtered.length === 0 && (
        <EmptyState
          icon={<Icon name="contacts" />}
          title="Nenhum membro encontrado."
          description={members.length > 0 ? 'Ajuste a busca ou os filtros.' : undefined}
        />
      )}

      {!team.isLoading && !team.isError && filtered.length > 0 && (
        <>
          <div className="hidden md:block">
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeaderCell>Nome</TableHeaderCell>
                  <TableHeaderCell>E-mail</TableHeaderCell>
                  <TableHeaderCell>Função</TableHeaderCell>
                  <TableHeaderCell>Status</TableHeaderCell>
                  <TableHeaderCell>Último login</TableHeaderCell>
                  {canManage && <TableHeaderCell />}
                </TableRow>
              </TableHead>
              <TableBody>
                {filtered.map((m) => (
                  <TeamRow
                    key={m.membership_id}
                    member={m}
                    canManage={canManage}
                    onEditRole={() => setRoleEditTarget(m)}
                    onToggleStatus={(status) => setPendingStatus({ member: m, status })}
                  />
                ))}
              </TableBody>
            </Table>
          </div>

          <div className="md:hidden space-y-3">
            {filtered.map((m) => (
              <TeamCard
                key={m.membership_id}
                member={m}
                canManage={canManage}
                onEditRole={() => setRoleEditTarget(m)}
                onToggleStatus={(status) => setPendingStatus({ member: m, status })}
              />
            ))}
          </div>
        </>
      )}
        </>
      )}

      {tab === 'invitations' && (
        <InvitationsTab
          invitations={invitations}
          canManage={canManage}
          canResend={canInvite}
          resendingId={resendInvitation.isPending ? resendInvitation.variables : undefined}
          onResend={(inv) => resendInvitation.mutate(inv.id)}
          onRevoke={setPendingRevokeInvite}
        />
      )}

      {inviteOpen && (
        <InviteModal
          roles={roles.data ?? []}
          isSaving={createInvitation.isPending}
          onCancel={() => setInviteOpen(false)}
          onConfirm={(email, roleKey) => createInvitation.mutate({ email, roleKey })}
        />
      )}
      </div>

      {pendingRevokeInvite && (
        <ConfirmDialog
          open
          title="Revogar convite"
          message={`O convite para ${pendingRevokeInvite.email} deixa de funcionar imediatamente.`}
          confirmLabel="Revogar convite"
          destructive
          isPending={revokeInvitation.isPending}
          onCancel={() => setPendingRevokeInvite(null)}
          onConfirm={() => revokeInvitation.mutate(pendingRevokeInvite.id)}
        />
      )}

      {roleEditTarget && (
        <RoleEditModal
          member={roleEditTarget}
          roles={roles.data ?? []}
          isSaving={changeRole.isPending}
          onCancel={() => setRoleEditTarget(null)}
          onConfirm={(roleKey) => changeRole.mutate({ membershipId: roleEditTarget.membership_id, roleKey })}
        />
      )}

      {pendingStatus && (
        <ConfirmDialog
          open
          title={statusConfirmTitle(pendingStatus.status)}
          message={statusConfirmDescription(pendingStatus.member, pendingStatus.status)}
          confirmLabel={statusConfirmTitle(pendingStatus.status)}
          destructive={pendingStatus.status !== 'active'}
          isPending={changeStatus.isPending}
          onCancel={() => setPendingStatus(null)}
          onConfirm={() =>
            changeStatus.mutate({ membershipId: pendingStatus.member.membership_id, status: pendingStatus.status })
          }
        />
      )}
    </SettingsShell>
  )
}

function SummaryCard({ icon, label, value }: { icon: IconName; label: string; value: number }) {
  return (
    <Card padding="compact">
      <div className="flex items-center gap-3">
        <div className="flex h-10 w-10 items-center justify-center rounded-control bg-accent-primary-soft text-accent-primary">
          <Icon name={icon} size={20} />
        </div>
        <div>
          <p className="text-sm text-text-secondary">{label}</p>
          <p className="text-section-md font-semibold text-text-primary">{value}</p>
        </div>
      </div>
    </Card>
  )
}

function memberActions(
  member: TeamMember,
  onEditRole: () => void,
  onToggleStatus: (status: MembershipStatus) => void,
): MenuAction[] {
  const actions: MenuAction[] = [
    { label: 'Alterar função', onSelect: onEditRole, disabled: member.status === 'revoked' },
  ]
  if (member.status === 'active') {
    actions.push({ label: 'Desativar acesso', onSelect: () => onToggleStatus('inactive') })
  } else if (member.status === 'inactive') {
    actions.push({ label: 'Reativar', onSelect: () => onToggleStatus('active') })
  }
  if (member.status !== 'revoked') {
    actions.push({ label: 'Remover do tenant', onSelect: () => onToggleStatus('revoked'), destructive: true })
  }
  return actions
}

function TeamRow({
  member,
  canManage,
  onEditRole,
  onToggleStatus,
}: {
  member: TeamMember
  canManage: boolean
  onEditRole: () => void
  onToggleStatus: (status: MembershipStatus) => void
}) {
  const role = ROLE_BADGE[member.role_key] ?? { label: member.role_name, variant: 'default' as const }
  const status = STATUS_BADGE[member.status]
  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-3">
          <Avatar alt={member.name || member.email} initials={initials(member.name, member.email)} size="sm" />
          <span className="font-medium text-text-primary">{member.name || member.email}</span>
        </div>
      </TableCell>
      <TableCell className="text-text-secondary">{member.email}</TableCell>
      <TableCell>
        <Badge variant={role.variant} size="sm">{role.label}</Badge>
      </TableCell>
      <TableCell>
        <StatusBadge status={status.tone} size="sm">{status.label}</StatusBadge>
      </TableCell>
      <TableCell className="text-text-secondary">{formatLastLogin(member.last_login_at)}</TableCell>
      {canManage && (
        <TableCell className="text-right">
          <DropdownMenu actions={memberActions(member, onEditRole, onToggleStatus)} />
        </TableCell>
      )}
    </TableRow>
  )
}

function TeamCard({
  member,
  canManage,
  onEditRole,
  onToggleStatus,
}: {
  member: TeamMember
  canManage: boolean
  onEditRole: () => void
  onToggleStatus: (status: MembershipStatus) => void
}) {
  const role = ROLE_BADGE[member.role_key] ?? { label: member.role_name, variant: 'default' as const }
  const status = STATUS_BADGE[member.status]
  return (
    <div className="rounded-card border border-border-subtle bg-surface p-4">
      <div className="flex items-center gap-3">
        <Avatar alt={member.name || member.email} initials={initials(member.name, member.email)} size="md" />
        <div className="min-w-0 flex-1">
          <p className="font-medium text-text-primary truncate">{member.name || member.email}</p>
          <p className="text-sm text-text-secondary truncate">{member.email}</p>
        </div>
        {canManage && <DropdownMenu actions={memberActions(member, onEditRole, onToggleStatus)} />}
      </div>
      <div className="mt-3 flex items-center gap-2">
        <Badge variant={role.variant} size="sm">{role.label}</Badge>
        <StatusBadge status={status.tone} size="sm">{status.label}</StatusBadge>
        <span className="ml-auto text-xs text-text-tertiary">{formatLastLogin(member.last_login_at)}</span>
      </div>
    </div>
  )
}

function RoleEditModal({
  member,
  roles,
  isSaving,
  onCancel,
  onConfirm,
}: {
  member: TeamMember
  roles: RoleOption[]
  isSaving: boolean
  onCancel: () => void
  onConfirm: (roleKey: string) => void
}) {
  const [selected, setSelected] = useState(member.role_key)
  return (
    <Modal
      open
      title="Alterar função"
      description={member.name || member.email}
      onClose={onCancel}
      footer={
        <>
          <Button variant="tertiary" onClick={onCancel} disabled={isSaving}>Cancelar</Button>
          <Button variant="primary" onClick={() => onConfirm(selected)} disabled={isSaving || selected === member.role_key}>
            {isSaving ? 'Salvando…' : 'Salvar'}
          </Button>
        </>
      }
    >
      <div className="space-y-2">
        {roles.map((r) => (
          <label
            key={r.key}
            className="flex items-center gap-3 rounded-control border border-border-subtle p-3 cursor-pointer hover:bg-surface-hover"
          >
            <input
              type="radio"
              name="role"
              value={r.key}
              checked={selected === r.key}
              onChange={() => setSelected(r.key)}
            />
            <span className="text-sm text-text-primary">{ROLE_DISPLAY_NAME[r.key] ?? r.name}</span>
          </label>
        ))}
      </div>
    </Modal>
  )
}

function statusConfirmTitle(status: MembershipStatus): string {
  if (status === 'active') return 'Reativar acesso'
  if (status === 'inactive') return 'Desativar acesso'
  return 'Remover do tenant'
}

function statusConfirmDescription(member: TeamMember, status: MembershipStatus): string {
  const who = member.name || member.email
  if (status === 'active') return `${who} volta a ter acesso à plataforma.`
  if (status === 'inactive') return `${who} perde acesso até ser reativado.`
  return `${who} perde acesso permanentemente. Esta ação não pode ser desfeita pela interface.`
}

function TeamSkeleton() {
  return (
    <div className="rounded-card border border-border-subtle bg-surface p-6 space-y-4">
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className="flex items-center gap-3">
          <Skeleton width="w-8" height="h-8" className="rounded-full shrink-0" />
          <Skeleton width="w-full" height="h-4" />
        </div>
      ))}
    </div>
  )
}

interface InvitationActionProps {
  canManage: boolean
  canResend: boolean
  resendingId?: string
  onResend: (invitation: Invitation) => void
  onRevoke: (invitation: Invitation) => void
}

function InvitationsTab({
  invitations,
  canManage,
  canResend,
  resendingId,
  onResend,
  onRevoke,
}: {
  invitations: ReturnType<typeof useQuery<Invitation[]>>
} & InvitationActionProps) {
  const items = invitations.data ?? []

  if (invitations.isLoading) return <TeamSkeleton />

  if (invitations.isError) {
    return (
      <ErrorState
        message={invitationErrorMessage(invitations.error, 'Não foi possível carregar os convites.')}
        action={{ label: 'Tentar novamente', onClick: () => void invitations.refetch() }}
      />
    )
  }

  if (items.length === 0) {
    return (
      <EmptyState
        icon={<Icon name="plus" />}
        title="Nenhum convite enviado ainda."
        description={canManage ? 'Use "Convidar usuário" para trazer alguém para a equipe.' : undefined}
      />
    )
  }

  return (
    <>
      <div className="hidden md:block">
        <Table>
          <TableHead>
            <TableRow>
              <TableHeaderCell>E-mail</TableHeaderCell>
              <TableHeaderCell>Função</TableHeaderCell>
              <TableHeaderCell>Status</TableHeaderCell>
              <TableHeaderCell>Expira</TableHeaderCell>
              <TableHeaderCell>Enviado em</TableHeaderCell>
              <TableHeaderCell>Enviado por</TableHeaderCell>
              {canManage && <TableHeaderCell />}
            </TableRow>
          </TableHead>
          <TableBody>
            {items.map((inv) => (
              <InvitationRow key={inv.id} invitation={inv} canManage={canManage} canResend={canResend} resendingId={resendingId} onResend={onResend} onRevoke={onRevoke} />
            ))}
          </TableBody>
        </Table>
      </div>

      <div className="md:hidden space-y-3">
        {items.map((inv) => (
          <InvitationCard key={inv.id} invitation={inv} canManage={canManage} canResend={canResend} resendingId={resendingId} onResend={onResend} onRevoke={onRevoke} />
        ))}
      </div>
    </>
  )
}

function invitationActions(
  invitation: Invitation,
  { canResend, resendingId, onResend, onRevoke }: Pick<InvitationActionProps, 'canResend' | 'resendingId' | 'onResend' | 'onRevoke'>,
): MenuAction[] {
  const actions: MenuAction[] = []
  // Reenviar reemite o link e renova o prazo; serve para pendente (também vencido) ou não entregue.
  if (canResend && (invitation.status === 'pending' || invitation.status === 'expired') && resendingId !== invitation.id) {
    actions.push({ label: 'Reenviar convite', onSelect: () => onResend(invitation) })
  }
  if (invitation.status === 'pending') {
    actions.push({ label: 'Revogar convite', onSelect: () => onRevoke(invitation), destructive: true })
  }
  if (invitation.invite_url) {
    // invite_url é um path relativo (o backend não conhece o host que o
    // navegador está usando); a origem certa é a desta própria aba.
    const fullLink = window.location.origin + invitation.invite_url
    actions.push({
      label: 'Copiar link (dev)',
      onSelect: () => void navigator.clipboard?.writeText(fullLink).catch(() => undefined),
    })
  }
  return actions
}

function sentAtLabel(invitation: Invitation): string {
  if (invitation.sent_at) return formatDateTime(invitation.sent_at)
  return invitation.status === 'pending' || invitation.status === 'expired' ? 'Não entregue' : '—'
}

function InvitationRow({
  invitation,
  canManage,
  ...rest
}: {
  invitation: Invitation
} & InvitationActionProps) {
  const status = INVITATION_STATUS_BADGE[invitation.status]
  const actions = invitationActions(invitation, { canResend: rest.canResend, resendingId: rest.resendingId, onResend: rest.onResend, onRevoke: rest.onRevoke })
  return (
    <TableRow>
      <TableCell className="text-text-primary">{invitation.email}</TableCell>
      <TableCell>
        <Badge variant={ROLE_BADGE[invitation.role_key]?.variant ?? 'default'} size="sm">
          {ROLE_DISPLAY_NAME[invitation.role_key] ?? invitation.role_name}
        </Badge>
      </TableCell>
      <TableCell>
        <StatusBadge status={status.tone} size="sm">{status.label}</StatusBadge>
      </TableCell>
      <TableCell className="text-text-secondary">
        {invitation.status === 'pending' ? formatExpiresIn(invitation.expires_at) : '—'}
      </TableCell>
      <TableCell className="text-text-secondary">{sentAtLabel(invitation)}</TableCell>
      <TableCell className="text-text-secondary">{invitation.created_by_email}</TableCell>
      {canManage && (
        <TableCell className="text-right">
          {actions.length > 0 && <DropdownMenu actions={actions} />}
        </TableCell>
      )}
    </TableRow>
  )
}

function InvitationCard({
  invitation,
  canManage,
  ...rest
}: {
  invitation: Invitation
} & InvitationActionProps) {
  const status = INVITATION_STATUS_BADGE[invitation.status]
  const actions = invitationActions(invitation, { canResend: rest.canResend, resendingId: rest.resendingId, onResend: rest.onResend, onRevoke: rest.onRevoke })
  return (
    <div className="rounded-card border border-border-subtle bg-surface p-4">
      <div className="flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <p className="font-medium text-text-primary truncate">{invitation.email}</p>
          <p className="text-sm text-text-secondary">
            {invitation.status === 'pending' ? formatExpiresIn(invitation.expires_at) : status.label}
            {' · '}Enviado em: {sentAtLabel(invitation)}
          </p>
        </div>
        {canManage && actions.length > 0 && <DropdownMenu actions={actions} />}
      </div>
      <div className="mt-3 flex items-center gap-2">
        <Badge variant={ROLE_BADGE[invitation.role_key]?.variant ?? 'default'} size="sm">
          {ROLE_DISPLAY_NAME[invitation.role_key] ?? invitation.role_name}
        </Badge>
        <StatusBadge status={status.tone} size="sm">{status.label}</StatusBadge>
      </div>
    </div>
  )
}

function InviteModal({
  roles,
  isSaving,
  onCancel,
  onConfirm,
}: {
  roles: RoleOption[]
  isSaving: boolean
  onCancel: () => void
  onConfirm: (email: string, roleKey: string) => void
}) {
  const [email, setEmail] = useState('')
  const [roleKey, setRoleKey] = useState(roles[0]?.key ?? 'tenant_agent')
  const [touched, setTouched] = useState(false)

  const emailValid = /\S+@\S+\.\S+/.test(email)

  return (
    <Modal
      open
      title="Convidar usuário"
      description="A pessoa recebe um link de convite para ingressar nesta equipe."
      onClose={onCancel}
      footer={
        <>
          <Button variant="tertiary" onClick={onCancel} disabled={isSaving}>Cancelar</Button>
          <Button
            variant="primary"
            onClick={() => {
              setTouched(true)
              if (emailValid) onConfirm(email.trim().toLowerCase(), roleKey)
            }}
            disabled={isSaving}
          >
            {isSaving ? 'Enviando…' : 'Enviar convite'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <div>
          <label htmlFor="invite-email" className="block text-sm font-medium text-text-primary mb-1">
            E-mail
          </label>
          <Input
            id="invite-email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            onBlur={() => setTouched(true)}
            error={touched && !emailValid ? 'Informe um e-mail válido.' : undefined}
            autoComplete="off"
          />
        </div>
        <div>
          <label htmlFor="invite-role" className="block text-sm font-medium text-text-primary mb-1">
            Função
          </label>
          <select
            id="invite-role"
            value={roleKey}
            onChange={(e) => setRoleKey(e.target.value)}
            className="h-10 w-full rounded-control border border-border-light bg-surface px-3 text-sm text-text-primary"
          >
            {roles.map((r) => (
              <option key={r.key} value={r.key}>
                {ROLE_DISPLAY_NAME[r.key] ?? r.name}
              </option>
            ))}
          </select>
        </div>
      </div>
    </Modal>
  )
}
