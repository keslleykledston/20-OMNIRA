import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import clsx from 'clsx'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Avatar, Badge, Button, EmptyState, ErrorState, Icon, Input, PermissionState, Skeleton } from '../components/primitives'
import { SettingsShell, SETTINGS_SECTIONS } from '../components/SettingsShell'
import { PersonSheet, type SheetSector } from '../components/people/PersonSheet'
import { InviteDialog } from '../components/people/InviteDialog'
import { initialsOf, ToneDot, TonePill } from '../components/people/controls'
import { agentsAPI } from '../lib/agents'
import { invitationErrorMessage, invitationsAPI, type AttendancePlan } from '../lib/invitations'
import {
  buildPeople,
  filterPeople,
  isHistory,
  NO_FILTER,
  readinessOf,
  summarize,
  type PeopleFilter,
  type PersonEntry,
  type Tone,
} from '../lib/peopleModel'
import { presenceAPI } from '../lib/presence'
import { attendsOf, changesOf, chosenQueues, seatOf, type SheetEdits } from '../lib/personDraft'
import { queueErrorMessage, queuesAPI } from '../lib/queues'
import { ROLE_DISPLAY_NAME } from '../lib/roles'
import { sectorsAPI } from '../lib/sectors'
import { getTenantId } from '../lib/session'
import { teamAPI, teamErrorMessage, type MembershipStatus } from '../lib/team'
import { useAccess } from '../lib/useAccess'
import { usePresenceEvents } from '../hooks/usePresenceEvents'

const ROLE_FILTERS = ['tenant_admin', 'tenant_supervisor', 'tenant_agent']

const SUMMARY: { tone: Tone | 'all'; label: string }[] = [
  { tone: 'all', label: 'Todas' },
  { tone: 'ok', label: 'Recebem conversas' },
  { tone: 'warn', label: 'Precisam de atenção' },
  { tone: 'neutral', label: 'Não atendem agora' },
  { tone: 'info', label: 'Convites enviados' },
]

const STATUS_NOTICE: Record<MembershipStatus, string> = {
  active: 'Acesso reativado.',
  inactive: 'Acesso suspenso. Função e filas ficaram guardadas.',
  revoked: 'A pessoa saiu desta instância.',
}

function agentActionError(err: unknown): string {
  const status = (err as { response?: { status?: number } })?.response?.status
  if (status === 403) return 'Você não tem permissão para mudar quem atende.'
  if (status === 404) return 'A pessoa ou a fila não está mais ativa. A tela foi atualizada.'
  return queueErrorMessage(err, 'Não foi possível salvar. Tente de novo.')
}

function PersonRow({ person, showAttendance, onOpen }: { person: PersonEntry; showAttendance: boolean; onOpen: () => void }) {
  const r = readinessOf(person)
  const agent = person.agent
  const attendanceKnown = showAttendance || person.state !== 'active'
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label={`Abrir a ficha de ${person.name}`}
      className={clsx(
        'grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 px-4 py-3 text-left transition-colors hover:bg-surface-hover',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent-primary',
        'md:grid-cols-[minmax(0,2.2fr)_minmax(0,0.9fr)_minmax(0,1.3fr)_minmax(0,1.6fr)_1rem]',
        isHistory(person) && 'opacity-70',
      )}
    >
      <span className="flex min-w-0 items-center gap-3">
        <Avatar alt={person.name} initials={initialsOf(person.name)} size="sm" />
        <span className="min-w-0">
          <span className="block truncate font-medium text-text-primary">{person.name}</span>
          {person.name !== person.email && <span className="block truncate text-xs text-text-secondary">{person.email}</span>}
        </span>
      </span>
      <span className="col-start-1 md:col-start-auto">
        <Badge size="sm" variant={person.roleKey === 'tenant_admin' ? 'info' : 'default'}>{person.roleLabel}</Badge>
      </span>
      <span className="col-start-1 flex flex-wrap items-center gap-x-2 gap-y-1 md:col-start-auto">
        {attendanceKnown ? <TonePill tone={r.tone}>{r.label}</TonePill> : <span className="text-text-tertiary">—</span>}
        {r.reason && <span className="text-xs text-status-warning-strong">{r.reason}</span>}
        {agent?.status === 'active' && person.state === 'active' && (
          <span className="inline-flex items-center gap-1 text-xs text-text-secondary">
            <ToneDot tone={person.online ? 'ok' : 'neutral'} />
            {person.online ? 'online' : 'offline'}
          </span>
        )}
      </span>
      <span className="col-start-1 flex flex-wrap gap-1 md:col-start-auto">
        {agent && agent.queues.length > 0 ? (
          agent.queues.map((q) => (
            <span
              key={q.memberId}
              title={q.available ? undefined : 'Indisponível nesta fila'}
              className={clsx(
                'rounded-md border border-border-subtle px-2 py-0.5 text-xs',
                q.available && agent.status === 'active' ? 'text-text-primary' : 'text-text-tertiary line-through',
              )}
            >
              {q.queueName}
            </span>
          ))
        ) : (
          <span className="text-text-tertiary">—</span>
        )}
      </span>
      <span aria-hidden="true" className="col-start-2 row-start-1 text-lg text-text-tertiary md:col-start-auto md:row-start-auto">›</span>
    </button>
  )
}

function ListSkeleton() {
  return (
    <div className="space-y-4 rounded-card border border-border-subtle bg-surface p-6" aria-busy="true">
      <span className="sr-only">Carregando</span>
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className="flex items-center gap-3">
          <Skeleton width="w-8" height="h-8" className="shrink-0 rounded-full" />
          <Skeleton width="w-full" height="h-4" />
        </div>
      ))}
    </div>
  )
}

export default function PeoplePage() {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const access = useAccess()
  const [params] = useSearchParams()

  const canSeeTeam = access.can('membership.read')
  const canSeeAgents = access.can('agent.read')
  const canManageMembers = access.can('membership.manage')
  const canManageAgents = access.can('agent.manage')
  const canInvite = canManageMembers && (access.data?.invitation_delivery_available ?? false)

  const team = useQuery({ queryKey: ['team', tenantId], queryFn: teamAPI.list, enabled: canSeeTeam, retry: false })
  const invitations = useQuery({ queryKey: ['team-invitations', tenantId], queryFn: invitationsAPI.list, enabled: canSeeTeam, retry: false })
  const roles = useQuery({ queryKey: ['team-roles', tenantId], queryFn: teamAPI.roles, enabled: canManageMembers, retry: false })
  const agents = useQuery({ queryKey: ['agents', tenantId], queryFn: agentsAPI.list, enabled: canSeeAgents, retry: false })
  const queues = useQuery({ queryKey: ['att-queues', tenantId], queryFn: queuesAPI.list, enabled: canSeeAgents, retry: false })
  const sectors = useQuery({ queryKey: ['att-sectors', tenantId], queryFn: sectorsAPI.list, enabled: canSeeAgents, retry: false })
  const presence = useQuery({ queryKey: ['agents-presence', tenantId], queryFn: presenceAPI.snapshot, enabled: canSeeAgents, retry: false })

  // Presence is read-only here (ADR-0010): the snapshot, then the SSE transitions.
  const [online, setOnline] = useState<Set<string>>(new Set())
  useEffect(() => {
    if (presence.data) setOnline(new Set(presence.data.online_agent_profile_ids))
  }, [presence.data])
  usePresenceEvents({
    tenantId: tenantId ?? '',
    enabled: canSeeAgents,
    onEvent: (event) =>
      setOnline((current) => {
        const next = new Set(current)
        if (event.status === 'online') next.add(event.agent_profile_id)
        else next.delete(event.agent_profile_id)
        return next
      }),
  })

  const [filter, setFilter] = useState<PeopleFilter>(NO_FILTER)
  const [openKey, setOpenKey] = useState<string | null>(() => params.get('pessoa'))
  const [inviteOpen, setInviteOpen] = useState(false)
  const [pageNotice, setPageNotice] = useState<string | null>(null)
  const [pageError, setPageError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [sheetError, setSheetError] = useState<string | null>(null)
  const [sheetNotice, setSheetNotice] = useState<string | null>(null)

  const attendanceKnown = canSeeAgents && agents.isSuccess
  const people = useMemo(
    () => buildPeople(team.data ?? [], attendanceKnown ? agents.data ?? [] : [], invitations.data ?? [], online),
    [team.data, agents.data, attendanceKnown, invitations.data, online],
  )
  const visible = filterPeople(people, filter)
  const counts = summarize(people)
  const historyCount = people.filter(isHistory).length
  const selected = people.find((p) => p.key === openKey) ?? null

  const sheetSectors = useMemo<SheetSector[] | null>(() => {
    if (!attendanceKnown || !queues.data) return null
    const byId = new Map(queues.data.map((q) => [q.id, { id: q.id, name: q.name, isDefault: q.is_default, mode: q.mode }]))
    const placed = new Set<string>()
    const groups: SheetSector[] = (sectors.data ?? []).map((s) => {
      const qs = s.queues.flatMap((sq) => {
        const q = byId.get(sq.id)
        return q ? [q] : []
      })
      qs.forEach((q) => placed.add(q.id))
      return { id: s.id, name: s.name, active: s.active, queues: qs }
    })
    const rest = queues.data.filter((q) => !placed.has(q.id)).map((q) => byId.get(q.id)!)
    if (rest.length > 0) groups.push({ id: null, name: groups.length > 0 ? 'Sem setor' : 'Filas', active: true, queues: rest })
    return groups
  }, [attendanceKnown, queues.data, sectors.data])

  const refreshAll = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ['team', tenantId] }),
      queryClient.invalidateQueries({ queryKey: ['team-invitations', tenantId] }),
      queryClient.invalidateQueries({ queryKey: ['agents', tenantId] }),
      queryClient.invalidateQueries({ queryKey: ['att-queues', tenantId] }),
    ])

  // One change at a time from the sheet: the control shows it is busy, success or failure is said in a sentence, and the list is read again either way.
  const run = async (key: string, action: () => Promise<unknown>, done: string | null, describe: (err: unknown) => string) => {
    setBusy(key)
    setSheetError(null)
    setSheetNotice(null)
    try {
      await action()
      setSheetNotice(done)
      await refreshAll()
    } catch (err) {
      setSheetError(describe(err))
      void refreshAll()
    } finally {
      setBusy(null)
    }
  }

  // "Salvar alterações" in the sheet: function first, then attendance, then the queues, each step only when it changed. The queue seats are
  // made to match the sheet against what the server has NOW (creating an attendant may already have placed them in the default overflow
  // queue). Whatever fails is said in a sentence; the list is read again either way, and what was not saved stays pending in the sheet.
  const saveSheet = async (p: PersonEntry, edits: SheetEdits): Promise<boolean> => {
    const changes = changesOf(p, edits)
    if (changes.length === 0 || !p.member) return true
    setBusy('save')
    setSheetError(null)
    setSheetNotice(null)
    let step: 'role' | 'agent' = 'role'
    try {
      const role = changes.find((c) => c.kind === 'role')
      if (role && role.kind === 'role') await teamAPI.updateRole(p.member.membership_id, role.to)
      step = 'agent'
      let profileId = p.agent?.profileId
      const attend = changes.find((c) => c.kind === 'attend')
      const turningOn = attend?.kind === 'attend' && attend.on
      const pausing = attend?.kind === 'attend' && !attend.on
      if (turningOn) {
        if (!profileId) profileId = (await agentsAPI.create(p.member.membership_id)).id
        else await agentsAPI.setStatus(profileId, 'active')
      }
      // queue edits are applied while the person still attends: before a pause, after switching on
      if (profileId && (attendsOf(p, edits) || pausing) && Object.keys(edits.seats).length > 0) {
        const now = (await agentsAPI.list()).find((a) => a.id === profileId)?.queues ?? []
        const onServer = new Map(now.map((q) => [q.queue_id, q]))
        for (const queueId of Object.keys(edits.seats)) {
          const want = seatOf(p, edits, queueId)
          const has = onServer.get(queueId)
          if (want.member && !has) await agentsAPI.addQueue(profileId, queueId, want.available, want.capacity)
          else if (!want.member && has) await agentsAPI.removeQueue(profileId, has.id)
          else if (want.member && has && (has.available !== want.available || has.capacity !== want.capacity)) {
            await agentsAPI.updateQueue(profileId, has.id, want.available, want.capacity)
          }
        }
        // a new attendant who chose queues keeps only those (the server may have added its default queue on its own, also in a first
        // save that stopped half way and is tried again: edits.fresh survives until everything was saved)
        if ((!p.agent || edits.fresh) && chosenQueues(edits).length > 0) {
          for (const q of now) if (!(q.queue_id in edits.seats)) await agentsAPI.removeQueue(profileId, q.id)
        }
      }
      if (pausing && profileId) await agentsAPI.setStatus(profileId, 'disabled')
      setSheetNotice('Alterações salvas.')
      return true
    } catch (err) {
      setSheetError(step === 'role' ? teamErrorMessage(err, 'Não foi possível alterar a função.') : agentActionError(err))
      return false
    } finally {
      await refreshAll()
      setBusy(null)
    }
  }

  const openPerson = (key: string) => {
    setSheetError(null)
    setSheetNotice(null)
    setInviteOpen(false)
    setOpenKey(key)
  }
  const closeSheet = useCallback(() => setOpenKey(null), [])

  const createInvitation = useMutation({
    mutationFn: ({ email, roleKey, plan }: { email: string; roleKey: string; plan?: AttendancePlan }) => invitationsAPI.create(email, roleKey, plan),
    onSuccess: (inv) => {
      setInviteOpen(false)
      setPageError(null)
      setPageNotice(inv.sent_at ? `Convite enviado para ${inv.email}.` : `Convite criado para ${inv.email}. O e-mail não foi enviado: abra a pessoa na lista para copiar o link.`)
      void queryClient.invalidateQueries({ queryKey: ['team-invitations', tenantId] })
    },
    onError: (err) => {
      setInviteOpen(false)
      setPageNotice(null)
      setPageError(invitationErrorMessage(err, 'Não foi possível enviar o convite.'))
      // 502: the invitation exists without delivery; the list must show it so it can be resent.
      void queryClient.invalidateQueries({ queryKey: ['team-invitations', tenantId] })
    },
  })

  const peopleSection = SETTINGS_SECTIONS.find((s) => s.key === 'people')
  const shell = (children: ReactNode) => (
    <SettingsShell
      sections={SETTINGS_SECTIONS}
      subsections={peopleSection?.subsections}
      title="Pessoas"
      description="Quem trabalha nesta instância, o que cada pessoa pode fazer e de quais filas recebe conversas."
      actions={
        canManageMembers ? (
          <Button
            variant="primary"
            onClick={() => setInviteOpen(true)}
            disabled={!canInvite}
            title={canInvite ? undefined : 'O envio de convites ainda não está configurado neste ambiente.'}
          >
            <Icon name="plus" size={16} />
            Adicionar pessoa
          </Button>
        ) : undefined
      }
    >
      {children}
    </SettingsShell>
  )

  if (!access.isLoading && !canSeeTeam) {
    return shell(<PermissionState message="Você não tem permissão para ver as pessoas desta instância." />)
  }

  const p = selected
  const sheet = p && (
    <PersonSheet
      person={p}
      readiness={readinessOf(p)}
      sectors={sheetSectors}
      roles={roles.data ?? []}
      can={{ manageMembers: canManageMembers, manageAgents: canManageAgents, resend: canInvite }}
      busy={busy}
      error={sheetError}
      notice={sheetNotice}
      onClose={closeSheet}
      onSave={(edits) => saveSheet(p, edits)}
      onSetStatus={(status) =>
        p.member && void run('status', () => teamAPI.updateStatus(p.member!.membership_id, status), STATUS_NOTICE[status],
          (e) => teamErrorMessage(e, 'Não foi possível concluir a operação.'))
      }
      onResend={() => p.invitation && void run('resend', () => invitationsAPI.resend(p.invitation!.id), 'Convite reenviado.', (e) => invitationErrorMessage(e, 'Não foi possível reenviar o convite.'))}
      onRevoke={() => p.invitation && void run('revoke', () => invitationsAPI.revoke(p.invitation!.id), 'Convite cancelado.', (e) => invitationErrorMessage(e, 'Não foi possível cancelar o convite.'))}
    />
  )

  const loading = access.isLoading || team.isLoading
  const filtered = filter.tone !== 'all' || filter.role !== '' || filter.queue !== '' || filter.text.trim() !== ''

  return shell(
    <div className="space-y-5">
      {pageNotice && (
        <div role="status" className="flex items-center justify-between gap-3 rounded-card border border-status-success-border bg-status-success-soft px-4 py-3 text-sm text-status-success">
          <span>{pageNotice}</span>
          <button type="button" className="underline" onClick={() => setPageNotice(null)}>Fechar</button>
        </div>
      )}
      {pageError && <ErrorState message={pageError} isDismissible onDismiss={() => setPageError(null)} />}

      {loading ? (
        <ListSkeleton />
      ) : team.isError ? (
        <ErrorState message={teamErrorMessage(team.error, 'Não foi possível carregar as pessoas.')} action={{ label: 'Tentar novamente', onClick: () => void team.refetch() }} />
      ) : (
        <>
          {attendanceKnown && (
            <div role="group" aria-label="Filtrar por situação" className="flex flex-wrap gap-2">
              {SUMMARY.map(({ tone, label }) => (
                <button
                  key={tone}
                  type="button"
                  aria-pressed={filter.tone === tone}
                  onClick={() => setFilter((f) => ({ ...f, tone }))}
                  className={clsx(
                    'inline-flex items-center gap-2 rounded-full border px-3 py-1.5 text-sm transition-colors',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary',
                    filter.tone === tone ? 'border-accent-primary bg-accent-primary-soft text-accent-primary' : 'border-border-subtle bg-surface text-text-secondary hover:bg-surface-hover',
                  )}
                >
                  {tone !== 'all' && <ToneDot tone={tone} />}
                  {label}
                  <span className="font-semibold tabular-nums text-text-primary">{counts[tone]}</span>
                </button>
              ))}
            </div>
          )}

          <div className="flex flex-wrap gap-2">
            <div className="min-w-[14rem] flex-1">
              <Input
                aria-label="Buscar pessoas"
                placeholder={attendanceKnown ? 'Buscar por nome, e-mail ou fila' : 'Buscar por nome ou e-mail'}
                value={filter.text}
                onChange={(e) => setFilter((f) => ({ ...f, text: e.target.value }))}
              />
            </div>
            <select
              aria-label="Filtrar por função"
              value={filter.role}
              onChange={(e) => setFilter((f) => ({ ...f, role: e.target.value }))}
              className="h-10 rounded-control border border-border-light bg-surface px-3 text-sm text-text-primary"
            >
              <option value="">Todas as funções</option>
              {ROLE_FILTERS.map((k) => <option key={k} value={k}>{ROLE_DISPLAY_NAME[k]}</option>)}
            </select>
            {attendanceKnown && (queues.data ?? []).length > 0 && (
              <select
                aria-label="Filtrar por fila"
                value={filter.queue}
                onChange={(e) => setFilter((f) => ({ ...f, queue: e.target.value }))}
                className="h-10 rounded-control border border-border-light bg-surface px-3 text-sm text-text-primary"
              >
                <option value="">Todas as filas</option>
                {(queues.data ?? []).map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
              </select>
            )}
          </div>

          {invitations.isError && <p className="text-xs text-text-secondary">Não foi possível carregar os convites. As pessoas abaixo são só as da equipe.</p>}

          {people.length === 0 ? (
            <EmptyState
              icon={<Icon name="contacts" />}
              title="Ninguém nesta instância ainda."
              description={canManageMembers ? 'Use “Adicionar pessoa” para convidar a primeira.' : undefined}
            />
          ) : visible.length === 0 ? (
            <EmptyState
              icon={<Icon name="contacts" />}
              title="Ninguém com esses filtros."
              action={filtered ? { label: 'Limpar filtros', onClick: () => setFilter((f) => ({ ...NO_FILTER, history: f.history })) } : undefined}
            />
          ) : (
            <div className="overflow-hidden rounded-card border border-border-subtle bg-surface">
              <div aria-hidden="true" className="hidden grid-cols-[minmax(0,2.2fr)_minmax(0,0.9fr)_minmax(0,1.3fr)_minmax(0,1.6fr)_1rem] gap-x-4 border-b border-border-subtle bg-surface-muted px-4 py-2 text-xs font-semibold text-text-secondary md:grid">
                <span>Pessoa</span>
                <span>Função</span>
                <span>Atendimento</span>
                <span>Filas</span>
                <span />
              </div>
              <ul aria-label="Pessoas da instância" className="divide-y divide-border-subtle">
                {visible.map((person) => (
                  <li key={person.key}>
                    <PersonRow person={person} showAttendance={attendanceKnown} onOpen={() => openPerson(person.key)} />
                  </li>
                ))}
              </ul>
            </div>
          )}

          {historyCount > 0 && (
            <button
              type="button"
              className="text-sm font-medium text-accent-primary hover:underline"
              onClick={() => setFilter((f) => ({ ...f, history: !f.history }))}
            >
              {filter.history ? 'Esconder removidos e convites cancelados' : `Mostrar removidos e convites cancelados (${historyCount})`}
            </button>
          )}
        </>
      )}

      {sheet}
      {inviteOpen && (
        <InviteDialog
          roles={roles.data ?? []}
          people={people}
          sectors={sheetSectors}
          canPlan={canManageAgents}
          isSaving={createInvitation.isPending}
          onCancel={() => setInviteOpen(false)}
          onConfirm={(email, roleKey, plan) => createInvitation.mutate({ email, roleKey, plan })}
          onOpenPerson={openPerson}
        />
      )}
    </div>,
  )
}
