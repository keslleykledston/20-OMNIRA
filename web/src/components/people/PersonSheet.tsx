import { useEffect, useId, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import clsx from 'clsx'
import { useQuery } from '@tanstack/react-query'
import { Avatar, Badge, Button, Drawer, Skeleton } from '../primitives'
import { calendarsAPI, shiftsAPI } from '../../lib/sectors'
import type { MembershipStatus, RoleOption } from '../../lib/team'
import type { QueueMode } from '../../lib/queues'
import type { PersonEntry, PersonQueue, Readiness } from '../../lib/peopleModel'
import { ROLE_DISPLAY_NAME, ROLE_SUMMARY } from '../../lib/roles'
import { getTenantId } from '../../lib/session'
import { MODE_LABEL } from '../queues/queueText'
import { CapacityStepper, initialsOf, Switch, ToneDot, TonePill } from './controls'
import { firstName, formatDateTime, formatExpiresIn, formatLastLogin } from './format'

export interface SheetQueue {
  id: string
  name: string
  isDefault: boolean
  mode: QueueMode
}

export interface SheetSector {
  id: string | null
  name: string
  active: boolean
  queues: SheetQueue[]
}

export interface PersonSheetActions {
  onChangeRole: (roleKey: string) => void
  onSetStatus: (status: MembershipStatus) => void
  onAttend: (on: boolean) => void
  onJoinQueue: (queueId: string) => void
  onLeaveQueue: (memberId: string) => void
  onUpdateQueue: (memberId: string, values: { available: boolean; capacity: number }) => void
  onResend: () => void
  onRevoke: () => void
}

interface PersonSheetProps extends PersonSheetActions {
  person: PersonEntry
  readiness: Readiness
  /** null when the queues are not readable here: the attendance part is then left out. */
  sectors: SheetSector[] | null
  roles: RoleOption[]
  can: { manageMembers: boolean; manageAgents: boolean; resend: boolean }
  busy: string | null
  error: string | null
  notice: string | null
  onClose: () => void
}

function Section({ title, hint, aside, children }: { title: string; hint?: string; aside?: ReactNode; children?: ReactNode }) {
  const id = useId()
  return (
    <section aria-labelledby={id} className="border-t border-border-subtle py-5">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h3 id={id} className="text-sm font-semibold text-text-primary">{title}</h3>
          {hint && <p className="mt-0.5 text-xs text-text-secondary">{hint}</p>}
        </div>
        {aside}
      </div>
      {children && <div className="mt-3">{children}</div>}
    </section>
  )
}

const BOX_TONE = {
  ok: 'bg-status-success-soft',
  warn: 'bg-status-warning-soft',
  info: 'bg-status-info-soft',
  neutral: 'bg-surface-muted',
} as const

function Box({ tone, title, children }: { tone: keyof typeof BOX_TONE; title: string; children?: ReactNode }) {
  return (
    <div className={clsx('mt-4 space-y-2 rounded-card p-4', BOX_TONE[tone])}>
      <p className="text-sm font-semibold text-text-primary">{title}</p>
      {children}
    </div>
  )
}

function InlineConfirm({ title, message, confirmLabel, busy, onCancel, onConfirm }: {
  title: string; message: string; confirmLabel: string; busy: boolean; onCancel: () => void; onConfirm: () => void
}) {
  return (
    <div role="alertdialog" aria-label={title} className="mt-3 space-y-3 rounded-control border border-status-danger-border bg-status-danger-soft p-3">
      <p className="text-sm font-semibold text-text-primary">{title}</p>
      <p className="text-sm text-text-secondary">{message}</p>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="secondary" onClick={onCancel} disabled={busy}>Cancelar</Button>
        <Button size="sm" variant="danger" onClick={onConfirm} isLoading={busy}>{confirmLabel}</Button>
      </div>
    </div>
  )
}

export function RoleOptions({ roles, name, value, onChange, disabled }: {
  roles: RoleOption[]; name: string; value: string; onChange: (key: string) => void; disabled?: boolean
}) {
  return (
    <div role="radiogroup" aria-label="Função" className="grid gap-2">
      {roles.map((r) => (
        <label
          key={r.key}
          className={clsx(
            'grid cursor-pointer grid-cols-[1rem_minmax(0,1fr)] items-start gap-x-3 gap-y-0.5 rounded-control border p-3 transition-colors',
            value === r.key ? 'border-accent-primary bg-accent-primary-soft' : 'border-border-subtle hover:bg-surface-hover',
            disabled && 'cursor-not-allowed opacity-60',
          )}
        >
          <input
            type="radio"
            name={name}
            value={r.key}
            checked={value === r.key}
            disabled={disabled}
            onChange={() => onChange(r.key)}
            className="mt-0.5 h-4 w-4 accent-[var(--color-accent-primary)]"
          />
          <span className="text-sm font-semibold text-text-primary">{ROLE_DISPLAY_NAME[r.key] ?? r.name}</span>
          {ROLE_SUMMARY[r.key] && <span className="col-start-2 text-xs text-text-secondary">{ROLE_SUMMARY[r.key]}</span>}
        </label>
      ))}
    </div>
  )
}

function RoleSection({ person, roles, canChange, busy, onSave }: { person: PersonEntry; roles: RoleOption[]; canChange: boolean; busy: boolean; onSave: (key: string) => void }) {
  const [draft, setDraft] = useState(person.roleKey)
  useEffect(() => setDraft(person.roleKey), [person.roleKey])
  if (!canChange || roles.length === 0) {
    return (
      <Section title="Função">
        <p className="text-sm text-text-primary"><span className="font-semibold">{person.roleLabel}</span>{ROLE_SUMMARY[person.roleKey] ? `: ${ROLE_SUMMARY[person.roleKey]}` : ''}</p>
      </Section>
    )
  }
  return (
    <Section title="Função" hint="Define o que a pessoa pode ver e mudar nesta instância.">
      <RoleOptions roles={roles} name={`funcao-${person.key}`} value={draft} onChange={setDraft} disabled={busy} />
      {draft !== person.roleKey && (
        <div className="mt-3 flex flex-wrap gap-2">
          <Button size="sm" onClick={() => onSave(draft)} isLoading={busy}>Salvar função</Button>
          <Button size="sm" variant="tertiary" onClick={() => setDraft(person.roleKey)} disabled={busy}>Cancelar</Button>
        </div>
      )}
    </Section>
  )
}

function ReadinessBox({ person, readiness, canManage, busy, onAttend }: { person: PersonEntry; readiness: Readiness; canManage: boolean; busy: boolean; onAttend: (on: boolean) => void }) {
  const who = firstName(person.name)
  const agent = person.agent
  if (!agent) {
    return (
      <Box tone="neutral" title={`${who} não atende conversas`}>
        <p className="text-sm text-text-secondary">{canManage ? 'Para receber conversas, a pessoa precisa atender e estar em ao menos uma fila.' : 'Quem decide é um administrador ou supervisor.'}</p>
        {canManage && <Button size="sm" onClick={() => onAttend(true)} isLoading={busy}>Passar a atender</Button>}
      </Box>
    )
  }
  if (agent.status === 'disabled') {
    return (
      <Box tone="neutral" title={`${who} está em pausa`}>
        <p className="text-sm text-text-secondary">Não recebe conversas novas. As filas escolhidas ficam guardadas para quando voltar.</p>
        {canManage && <Button size="sm" onClick={() => onAttend(true)} isLoading={busy}>Voltar a atender</Button>}
      </Box>
    )
  }
  const checks: [string, boolean][] = [
    ['Recebe conversas', true],
    ['Está em ao menos uma fila', agent.queues.length > 0],
    ['Disponível em ao menos uma fila', agent.queues.some((q) => q.available)],
  ]
  return (
    <Box tone={readiness.tone} title={readiness.tone === 'ok' ? `${who} recebe conversas` : `${who} ainda não recebe conversas`}>
      <ul className="space-y-1">
        {checks.map(([label, ok]) => (
          <li key={label} className={clsx('flex items-center gap-2 text-sm', ok ? 'text-text-primary' : 'font-semibold text-status-warning-strong')}>
            <span aria-hidden="true" className={clsx('inline-flex h-4 w-4 items-center justify-center rounded-full text-[10px] font-bold', ok ? 'bg-status-success text-white' : 'border-2 border-status-warning')}>{ok ? '✓' : ''}</span>
            {label}
            {!ok && <span className="sr-only">(falta)</span>}
          </li>
        ))}
      </ul>
      <p className="flex items-center gap-2 text-xs text-text-secondary"><ToneDot tone={person.online ? 'ok' : 'neutral'} />{person.online ? 'Online agora' : 'Offline agora'}</p>
    </Box>
  )
}

function QueueChoice({ queue, membership, canManage, busy, onJoin, onLeave, onUpdate }: {
  queue: SheetQueue
  membership?: PersonQueue
  canManage: boolean
  busy: boolean
  onJoin: () => void
  onLeave: () => void
  onUpdate: (values: { available: boolean; capacity: number }) => void
}) {
  const disabled = !canManage || busy
  return (
    <li className={clsx('rounded-control border p-3', membership ? 'border-accent-primary bg-accent-primary-soft' : 'border-border-subtle')}>
      <label className={clsx('flex items-start gap-3', disabled ? 'cursor-default' : 'cursor-pointer')}>
        <input
          type="checkbox"
          checked={!!membership}
          disabled={disabled}
          onChange={(e) => (e.target.checked ? onJoin() : onLeave())}
          aria-label={`Participa da fila ${queue.name}`}
          className="mt-0.5 h-4 w-4 accent-[var(--color-accent-primary)]"
        />
        <span className="min-w-0">
          <span className="flex flex-wrap items-center gap-1.5 text-sm font-semibold text-text-primary">
            {queue.name}
            {queue.isDefault && <Badge size="sm" variant="info">Padrão</Badge>}
          </span>
          <span className="block text-xs text-text-tertiary">{MODE_LABEL[queue.mode]}</span>
        </span>
      </label>
      {membership && (
        <div className="mt-3 flex flex-wrap items-center gap-x-6 gap-y-2 border-t border-border-subtle pl-7 pt-3 text-sm text-text-secondary">
          <span className="inline-flex items-center gap-2">
            <Switch
              checked={membership.available}
              onChange={(available) => onUpdate({ available, capacity: membership.capacity })}
              label={`Disponível na fila ${queue.name}`}
              disabled={disabled}
            />
            {membership.available ? 'Disponível' : 'Indisponível'}
          </span>
          <span className="inline-flex items-center gap-2">
            Até
            <CapacityStepper
              value={membership.capacity}
              onChange={(capacity) => onUpdate({ available: membership.available, capacity })}
              label={`Capacidade na fila ${queue.name}`}
              disabled={disabled}
            />
            ao mesmo tempo
          </span>
        </div>
      )}
    </li>
  )
}

function AttendSection({ person, sectors, canManage, busy, actions }: {
  person: PersonEntry
  sectors: SheetSector[]
  canManage: boolean
  busy: string | null
  actions: PersonSheetActions
}) {
  const agent = person.agent
  const on = agent?.status === 'active'
  const mine = new Map((agent?.queues ?? []).map((q) => [q.queueId, q]))
  const anyQueue = sectors.some((s) => s.queues.length > 0)
  return (
    <Section
      title="Recebe conversas"
      hint="Desligar pausa a pessoa sem perder as filas."
      aside={<Switch checked={on} onChange={actions.onAttend} label="Recebe conversas" disabled={!canManage || busy === 'attend'} />}
    >
      {agent?.status === 'disabled' && agent.queues.length > 0 && (
        <p className="text-sm text-text-secondary">Filas guardadas: {agent.queues.map((q) => q.queueName).join(', ')}.</p>
      )}
      {on && !anyQueue && (
        <p className="text-sm text-text-secondary">
          Nenhuma fila criada ainda. <Link to="/settings/attendance/sectors" className="text-accent-primary hover:underline">Criar em Atendimento › Filas e setores</Link>
        </p>
      )}
      {on && anyQueue && (
        <div className="space-y-4">
          {sectors.filter((s) => s.queues.length > 0).map((s) => (
            <div key={s.id ?? 'sem-setor'}>
              <h4 className="mb-2 text-xs font-semibold uppercase tracking-wide text-text-tertiary">
                {s.name}{!s.active && ' · setor inativo'}
              </h4>
              <ul className="space-y-2">
                {s.queues.map((q) => {
                  const m = mine.get(q.id)
                  return (
                    <QueueChoice
                      key={q.id}
                      queue={q}
                      membership={m}
                      canManage={canManage}
                      busy={busy === `join:${q.id}` || (m !== undefined && busy === `q:${m.memberId}`)}
                      onJoin={() => actions.onJoinQueue(q.id)}
                      onLeave={() => m && actions.onLeaveQueue(m.memberId)}
                      onUpdate={(v) => m && actions.onUpdateQueue(m.memberId, v)}
                    />
                  )
                })}
              </ul>
            </div>
          ))}
        </div>
      )}
    </Section>
  )
}

function ShiftSection({ userId, sectors }: { userId: string; sectors: SheetSector[] }) {
  const tenantId = getTenantId()
  const shifts = useQuery({ queryKey: ['att-shifts', tenantId, userId], queryFn: () => shiftsAPI.list(userId), retry: false })
  const calendars = useQuery({ queryKey: ['att-calendars', tenantId], queryFn: calendarsAPI.list, retry: false })
  const calendarName = (id: string) => calendars.data?.items.find((c) => c.id === id)?.name ?? 'horário removido'
  const scope = (s: { queue_id?: string; sector_id?: string }) => {
    if (s.queue_id) return `na fila ${sectors.flatMap((x) => x.queues).find((q) => q.id === s.queue_id)?.name ?? 'removida'}`
    if (s.sector_id) return `no setor ${sectors.find((x) => x.id === s.sector_id)?.name ?? 'removido'}`
    return 'em todas as filas'
  }
  return (
    <Section
      title="Escala"
      aside={<Link to="/settings/attendance/shifts" className="shrink-0 text-sm font-medium text-accent-primary hover:underline">Editar escala</Link>}
    >
      {shifts.isLoading ? (
        <Skeleton height="h-4" />
      ) : shifts.isError ? (
        <p className="text-sm text-text-secondary">Não foi possível ler a escala.</p>
      ) : (shifts.data ?? []).length === 0 ? (
        <p className="text-sm text-text-secondary">Sem escala própria: atende 24 h (padrão), sempre dentro do horário do setor.</p>
      ) : (
        <ul className="space-y-1 text-sm text-text-primary">
          {(shifts.data ?? []).map((s, i) => <li key={i}>{calendarName(s.calendar_id)} {scope(s)}</li>)}
        </ul>
      )}
    </Section>
  )
}

function AccessSection({ person, busy, onSetStatus }: { person: PersonEntry; busy: boolean; onSetStatus: (s: MembershipStatus) => void }) {
  const [confirm, setConfirm] = useState<null | 'inactive' | 'revoked'>(null)
  const who = person.name
  if (person.state === 'revoked') {
    return (
      <Section title="Acesso">
        <p className="text-sm text-text-secondary">Acesso removido. Para voltar, a pessoa precisa de um convite novo.</p>
      </Section>
    )
  }
  return (
    <Section title="Acesso">
      <div className="space-y-3 text-sm">
        {person.state === 'active' ? (
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-text-secondary">Suspender sem perder função e filas</span>
            <Button size="sm" variant="secondary" onClick={() => setConfirm('inactive')} disabled={busy}>Suspender acesso</Button>
          </div>
        ) : (
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-text-secondary">O acesso está suspenso.</span>
            <Button size="sm" onClick={() => onSetStatus('active')} isLoading={busy}>Reativar acesso</Button>
          </div>
        )}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-text-secondary">Tirar a pessoa desta instância</span>
          <Button size="sm" variant="danger" onClick={() => setConfirm('revoked')} disabled={busy}>Remover da instância</Button>
        </div>
      </div>
      {confirm === 'inactive' && (
        <InlineConfirm
          title={`Suspender o acesso de ${who}?`}
          message="A pessoa não entra mais nesta instância nem recebe conversas até ser reativada. Função e filas ficam guardadas."
          confirmLabel="Suspender acesso"
          busy={busy}
          onCancel={() => setConfirm(null)}
          onConfirm={() => { onSetStatus('inactive'); setConfirm(null) }}
        />
      )}
      {confirm === 'revoked' && (
        <InlineConfirm
          title={`Remover ${who} desta instância?`}
          message="A pessoa perde o acesso e deixa de receber conversas. Não dá para desfazer pela tela: para voltar, envie um convite novo."
          confirmLabel="Remover da instância"
          busy={busy}
          onCancel={() => setConfirm(null)}
          onConfirm={() => { onSetStatus('revoked'); setConfirm(null) }}
        />
      )}
    </Section>
  )
}

function InvitationBody({ person, readiness, can, busy, actions }: {
  person: PersonEntry; readiness: Readiness; can: PersonSheetProps['can']; busy: string | null; actions: PersonSheetActions
}) {
  const inv = person.invitation!
  const [confirm, setConfirm] = useState(false)
  const [copied, setCopied] = useState(false)
  const open = inv.status === 'pending' || inv.status === 'expired'
  const link = inv.invite_url ? window.location.origin + inv.invite_url : null
  return (
    <>
      <Box tone={readiness.tone} title={readiness.label}>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
          <dt className="text-text-secondary">E-mail</dt>
          <dd className="text-text-primary">{inv.sent_at ? `enviado em ${formatDateTime(inv.sent_at)}` : 'não entregue'}</dd>
          {inv.status === 'pending' && (<><dt className="text-text-secondary">Vence</dt><dd className="text-text-primary">{formatExpiresIn(inv.expires_at)}</dd></>)}
          <dt className="text-text-secondary">Enviado por</dt>
          <dd className="truncate text-text-primary">{inv.created_by_email}</dd>
        </dl>
        {readiness.reason && <p className="text-sm text-text-secondary">{readiness.reason}.</p>}
      </Box>
      <Section title="Função ao entrar" hint={open ? 'Para mudar, cancele e envie um convite novo.' : undefined}>
        <p className="text-sm text-text-primary"><span className="font-semibold">{person.roleLabel}</span>{ROLE_SUMMARY[person.roleKey] ? `: ${ROLE_SUMMARY[person.roleKey]}` : ''}</p>
      </Section>
      {open && (
        <Section title="Depois do aceite">
          <p className="text-sm text-text-secondary">Volte a esta ficha, ligue “Recebe conversas” e escolha as filas da pessoa.</p>
        </Section>
      )}
      {open && can.manageMembers && (
        <Section title="Convite">
          <div className="flex flex-wrap gap-2">
            {can.resend && <Button size="sm" variant="secondary" onClick={actions.onResend} isLoading={busy === 'resend'}>Reenviar convite</Button>}
            {link && (
              <Button size="sm" variant="secondary" onClick={() => void navigator.clipboard?.writeText(link).then(() => setCopied(true), () => undefined)}>
                {copied ? 'Link copiado' : 'Copiar link (dev)'}
              </Button>
            )}
            {inv.status === 'pending' && <Button size="sm" variant="danger" onClick={() => setConfirm(true)} disabled={busy === 'revoke'}>Cancelar convite</Button>}
          </div>
          {!can.resend && <p className="mt-2 text-xs text-text-tertiary">O envio de convites não está configurado neste ambiente.</p>}
          {confirm && (
            <InlineConfirm
              title={`Cancelar o convite de ${inv.email}?`}
              message="O link do convite deixa de funcionar na hora."
              confirmLabel="Cancelar convite"
              busy={busy === 'revoke'}
              onCancel={() => setConfirm(false)}
              onConfirm={() => { actions.onRevoke(); setConfirm(false) }}
            />
          )}
        </Section>
      )}
    </>
  )
}

export function PersonSheet(props: PersonSheetProps) {
  const { person, readiness, sectors, roles, can, busy, error, notice, onClose } = props
  const member = person.member
  return (
    <Drawer open title={`Ficha de ${person.name}`} onClose={onClose} size="lg">
      <div className="px-5 pb-8">
        <header className="flex items-start gap-3 pb-4 pr-10 pt-5">
          <Avatar alt={person.name} initials={initialsOf(person.name)} size="lg" />
          <div className="min-w-0 flex-1 space-y-1">
            <h2 className="truncate text-lg font-semibold text-text-primary">{person.name}</h2>
            {person.name !== person.email && <p className="truncate text-sm text-text-secondary">{person.email}</p>}
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 pt-1 text-xs text-text-secondary">
              <TonePill tone={readiness.tone}>{readiness.label}</TonePill>
              {member && <span>último acesso: {formatLastLogin(member.last_login_at)}</span>}
            </div>
          </div>
        </header>
        {notice && <p role="status" className="rounded-control border border-status-success-border bg-status-success-soft px-3 py-2 text-sm text-status-success">{notice}</p>}
        {error && <p role="alert" className="mt-2 rounded-control border border-status-danger-border bg-status-danger-soft px-3 py-2 text-sm text-status-danger">{error}</p>}
        {person.invitation ? (
          <InvitationBody person={person} readiness={readiness} can={can} busy={busy} actions={props} />
        ) : (
          <>
            {person.state === 'active' && sectors && (
              <ReadinessBox person={person} readiness={readiness} canManage={can.manageAgents} busy={busy === 'attend'} onAttend={props.onAttend} />
            )}
            <div className="mt-4">
              <RoleSection person={person} roles={roles} canChange={can.manageMembers && person.state !== 'revoked'} busy={busy === 'role'} onSave={props.onChangeRole} />
              {person.state === 'active' && sectors && (
                <AttendSection person={person} sectors={sectors} canManage={can.manageAgents} busy={busy} actions={props} />
              )}
              {person.state === 'active' && sectors && person.agent && member && <ShiftSection userId={member.user_id} sectors={sectors} />}
              {can.manageMembers && <AccessSection person={person} busy={busy === 'status'} onSetStatus={props.onSetStatus} />}
            </div>
          </>
        )}
      </div>
    </Drawer>
  )
}
