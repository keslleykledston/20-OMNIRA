import { useEffect, useId, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import clsx from 'clsx'
import { useQuery } from '@tanstack/react-query'
import { Avatar, Badge, Button, Drawer, Skeleton } from '../primitives'
import { calendarsAPI, shiftsAPI } from '../../lib/sectors'
import type { MembershipStatus, RoleOption } from '../../lib/team'
import type { QueueMode } from '../../lib/queues'
import type { PersonEntry, Readiness } from '../../lib/peopleModel'
import { attendsOf, changesOf, currentSeat, describeChange, NO_EDITS, roleOf, seatOf, type SeatChoice, type SheetEdits } from '../../lib/personDraft'
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
  /** Applies function, attendance and queue changes together; true when everything was saved. */
  onSave: (edits: SheetEdits) => Promise<boolean>
  onSetStatus: (status: MembershipStatus) => void
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

function RoleSection({ person, roles, canChange, busy, value, onChange }: {
  person: PersonEntry; roles: RoleOption[]; canChange: boolean; busy: boolean; value: string; onChange: (key: string) => void
}) {
  if (!canChange || roles.length === 0) {
    return (
      <Section title="Função">
        <p className="text-sm text-text-primary"><span className="font-semibold">{person.roleLabel}</span>{ROLE_SUMMARY[person.roleKey] ? `: ${ROLE_SUMMARY[person.roleKey]}` : ''}</p>
      </Section>
    )
  }
  return (
    <Section title="Função" hint="Define o que a pessoa pode ver e mudar nesta instância.">
      <RoleOptions roles={roles} name={`funcao-${person.key}`} value={value} onChange={onChange} disabled={busy} />
    </Section>
  )
}

function ReadinessBox({ person, readiness, canManage, busy, pendingOn, onAttend }: {
  person: PersonEntry; readiness: Readiness; canManage: boolean; busy: boolean; pendingOn: boolean; onAttend: (on: boolean) => void
}) {
  const who = firstName(person.name)
  const agent = person.agent
  if (!agent) {
    const text = pendingOn
      ? 'Escolha as filas abaixo e salve as alterações.'
      : canManage ? 'Para receber conversas, a pessoa precisa atender e estar em ao menos uma fila.' : 'Quem decide é um administrador ou supervisor.'
    return (
      <Box tone="neutral" title={`${who} não atende conversas`}>
        <p className="text-sm text-text-secondary">{text}</p>
        {canManage && !pendingOn && <Button size="sm" onClick={() => onAttend(true)} disabled={busy}>Passar a atender</Button>}
      </Box>
    )
  }
  if (agent.status === 'disabled') {
    const text = pendingOn ? 'Volta a atender quando você salvar as alterações.' : 'Não recebe conversas novas. As filas escolhidas ficam guardadas para quando voltar.'
    return (
      <Box tone="neutral" title={`${who} está em pausa`}>
        <p className="text-sm text-text-secondary">{text}</p>
        {canManage && !pendingOn && <Button size="sm" onClick={() => onAttend(true)} disabled={busy}>Voltar a atender</Button>}
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

function QueueRow({ queue, seat, changed, canManage, busy, onChange }: {
  queue: SheetQueue
  seat: SeatChoice
  changed: boolean
  canManage: boolean
  busy: boolean
  onChange: (values: Partial<SeatChoice>) => void
}) {
  const disabled = !canManage || busy
  return (
    <li className={clsx('flex flex-wrap items-center gap-x-4 gap-y-1.5 px-3 py-2', changed ? 'bg-status-warning-soft' : seat.member && 'bg-accent-primary-soft')}>
      <label className={clsx('flex max-w-full shrink-0 grow items-center gap-2.5', disabled ? 'cursor-default' : 'cursor-pointer')}>
        <input
          type="checkbox"
          checked={seat.member}
          disabled={disabled}
          onChange={(e) => onChange({ member: e.target.checked })}
          aria-label={`Participa da fila ${queue.name}`}
          className="h-4 w-4 shrink-0 accent-[var(--color-accent-primary)]"
        />
        <span className="truncate text-sm font-medium text-text-primary">{queue.name}</span>
        {queue.isDefault && <Badge size="sm" variant="info">Padrão</Badge>}
        <span className="shrink-0 text-xs text-text-tertiary">{MODE_LABEL[queue.mode]}</span>
        {changed && <span className="sr-only">(alteração não salva)</span>}
      </label>
      {seat.member && (
        <span className="flex items-center gap-3 text-xs text-text-secondary">
          <span className="inline-flex items-center gap-1.5">
            <Switch size="sm" checked={seat.available} onChange={(available) => onChange({ available })} label={`Disponível na fila ${queue.name}`} disabled={disabled} />
            {seat.available ? 'Disponível' : 'Indisponível'}
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span title="Conversas ao mesmo tempo nesta fila (0 = sem limite)">Simultâneas</span>
            <CapacityStepper size="sm" value={seat.capacity} onChange={(capacity) => onChange({ capacity })} label={`Capacidade na fila ${queue.name}`} disabled={disabled} />
          </span>
        </span>
      )}
    </li>
  )
}

function AttendSection({ person, sectors, canManage, busy, edits, onEdit }: {
  person: PersonEntry
  sectors: SheetSector[]
  canManage: boolean
  busy: boolean
  edits: SheetEdits
  onEdit: (next: SheetEdits) => void
}) {
  const agent = person.agent
  const on = attendsOf(person, edits)
  const anyQueue = sectors.some((s) => s.queues.length > 0)
  const changed = new Set(changesOf(person, edits).flatMap((c) => ('queueId' in c ? [c.queueId] : [])))
  const setSeat = (queueId: string, values: Partial<SeatChoice>) =>
    onEdit({ ...edits, seats: { ...edits.seats, [queueId]: { ...edits.seats[queueId], ...values } } })
  const chosen = sectors.flatMap((s) => s.queues).filter((q) => seatOf(person, edits, q.id).member).length
  return (
    <Section
      title="Recebe conversas"
      hint="Desligar pausa a pessoa sem perder as filas."
      aside={<Switch checked={on} onChange={(v) => onEdit({ ...edits, attends: v, fresh: v && !person.agent ? true : edits.fresh })} label="Recebe conversas" disabled={!canManage || busy} />}
    >
      {!on && agent && agent.queues.length > 0 && (
        <p className="text-sm text-text-secondary">Filas guardadas: {agent.queues.map((q) => q.queueName).join(', ')}.</p>
      )}
      {on && !anyQueue && (
        <p className="text-sm text-text-secondary">
          Nenhuma fila criada ainda. <Link to="/settings/attendance/sectors" className="text-accent-primary hover:underline">Criar em Atendimento › Filas e setores</Link>
        </p>
      )}
      {on && anyQueue && (
        <div className="space-y-3">
          {!agent && chosen === 0 && (
            <p className="text-xs text-text-tertiary">Sem fila escolhida, a pessoa entra na fila padrão de transbordo da empresa, se houver uma.</p>
          )}
          {sectors.filter((s) => s.queues.length > 0).map((s) => (
            <div key={s.id ?? 'sem-setor'}>
              <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-text-tertiary">
                {s.name}{!s.active && ' · setor inativo'}
              </h4>
              <ul aria-label={`Filas de ${s.name}`} className="divide-y divide-border-subtle overflow-hidden rounded-control border border-border-subtle">
                {s.queues.map((q) => (
                  <QueueRow
                    key={q.id}
                    queue={q}
                    seat={seatOf(person, edits, q.id)}
                    changed={changed.has(q.id)}
                    canManage={canManage}
                    busy={busy}
                    onChange={(v) => setSeat(q.id, v)}
                  />
                ))}
              </ul>
            </div>
          ))}
          <p className="text-xs text-text-tertiary">Simultâneas = conversas ao mesmo tempo em cada fila; 0 = sem limite.</p>
        </div>
      )}
    </Section>
  )
}

type SaveMode = 'idle' | 'confirm' | 'leave'

// Pending changes stay in the sheet until "Salvar alterações" and its confirmation; closing with changes asks first.
function SaveBar({ person, sectors, edits, busy, mode, onMode, onDiscard, onSave, onCloseAnyway }: {
  person: PersonEntry
  sectors: SheetSector[] | null
  edits: SheetEdits
  busy: boolean
  mode: SaveMode
  onMode: (m: SaveMode) => void
  onDiscard: () => void
  onSave: () => void
  onCloseAnyway: () => void
}) {
  const changes = changesOf(person, edits)
  if (changes.length === 0) return null
  const names = new Map((sectors ?? []).flatMap((s) => s.queues).map((q) => [q.id, q.name]))
  const queueName = (id: string) => names.get(id) ?? currentSeat(person, id)?.queueName ?? 'sem nome'
  const lines = changes.map((c) => describeChange(c, queueName))
  const freshWithoutQueue = changes.some((c) => c.kind === 'attend' && c.on && c.fresh) && !changes.some((c) => c.kind === 'join')
  const count = `${changes.length} ${changes.length === 1 ? 'alteração não salva' : 'alterações não salvas'}`
  return (
    <div className="sticky bottom-0 z-10 -mx-5 border-t border-border-subtle bg-surface px-5 py-3 shadow-[0_-4px_12px_rgba(0,0,0,0.06)]">
      {mode === 'confirm' ? (
        <div role="alertdialog" aria-label={`Aplicar as alterações em ${person.name}?`} className="space-y-3">
          <p className="text-sm font-semibold text-text-primary">Aplicar as alterações em {person.name}?</p>
          <ul className="list-disc space-y-1 pl-5 text-sm text-text-secondary">
            {lines.map((l) => <li key={l}>{l}</li>)}
            {freshWithoutQueue && <li>Sem fila escolhida: entra na fila padrão de transbordo da empresa, se houver uma.</li>}
          </ul>
          <div className="flex flex-wrap justify-end gap-2">
            <Button size="sm" variant="secondary" onClick={() => onMode('idle')} disabled={busy}>Voltar</Button>
            <Button size="sm" onClick={onSave} isLoading={busy}>Aplicar alterações</Button>
          </div>
        </div>
      ) : mode === 'leave' ? (
        <div role="alertdialog" aria-label="Fechar sem salvar?" className="space-y-3">
          <p className="text-sm font-semibold text-text-primary">Fechar sem salvar?</p>
          <p className="text-sm text-text-secondary">{changes.length === 1 ? 'A alteração não salva será descartada.' : `As ${changes.length} alterações não salvas serão descartadas.`}</p>
          <div className="flex flex-wrap justify-end gap-2">
            <Button size="sm" variant="secondary" onClick={() => onMode('idle')} disabled={busy}>Continuar editando</Button>
            <Button size="sm" variant="danger" onClick={onCloseAnyway} disabled={busy}>Fechar sem salvar</Button>
          </div>
        </div>
      ) : (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span role="status" className="text-sm font-medium text-text-primary">{count}</span>
          <div className="flex gap-2">
            <Button size="sm" variant="tertiary" onClick={onDiscard} disabled={busy}>Descartar</Button>
            <Button size="sm" onClick={() => onMode('confirm')} disabled={busy}>Salvar alterações</Button>
          </div>
        </div>
      )}
    </div>
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

function planSentence(inv: NonNullable<PersonEntry['invitation']>, sectors: SheetSector[] | null): string {
  if (!inv.attends) return 'Volte a esta ficha depois do aceite, ligue “Recebe conversas” e escolha as filas da pessoa.'
  const planned = inv.queue_ids ?? []
  // Queue names unknown here (not loaded, or no permission to read them): say how many, never claim the overflow queue.
  if (sectors === null && planned.length > 0) {
    return `Ao aceitar, a pessoa já recebe conversas de ${planned.length} ${planned.length === 1 ? 'fila escolhida' : 'filas escolhidas'} no convite.`
  }
  const names = new Map((sectors ?? []).flatMap((s) => s.queues).map((q) => [q.id, q.name]))
  const chosen = planned.map((id) => names.get(id)).filter((n): n is string => !!n)
  if (chosen.length === 0) return 'Ao aceitar, a pessoa já passa a atender, pela fila padrão de transbordo da empresa.'
  const list = chosen.length === 1 ? chosen[0] : `${chosen.slice(0, -1).join(', ')} e ${chosen[chosen.length - 1]}`
  const cap = inv.queue_capacity ?? 1
  const limit = cap === 0 ? 'sem limite de conversas ao mesmo tempo' : `até ${cap} ${cap === 1 ? 'conversa' : 'conversas'} ao mesmo tempo`
  return `Ao aceitar, a pessoa já recebe conversas de ${list}, ${limit} em cada fila.`
}

function InvitationBody({ person, readiness, sectors, can, busy, actions }: {
  person: PersonEntry; readiness: Readiness; sectors: SheetSector[] | null; can: PersonSheetProps['can']; busy: string | null; actions: PersonSheetActions
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
          <p className="text-sm text-text-secondary">{planSentence(inv, sectors)}</p>
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
            {/* an expired invitation is still open on the server: cancelling it also turns off its unused sign-in account (ADR-0046) */}
            <Button size="sm" variant="danger" onClick={() => setConfirm(true)} disabled={busy === 'revoke'}>Cancelar convite</Button>
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
  const [edits, setEdits] = useState<SheetEdits>(NO_EDITS)
  const [mode, setMode] = useState<SaveMode>('idle')
  useEffect(() => {
    setEdits(NO_EDITS)
    setMode('idle')
  }, [person.key])
  const dirty = changesOf(person, edits).length > 0
  const saving = busy === 'save'
  const edit = (next: SheetEdits) => {
    setEdits(next)
    setMode('idle')
  }
  const save = async () => {
    if (await props.onSave(edits)) setEdits(NO_EDITS)
    setMode('idle')
  }
  const requestClose = () => {
    if (saving) return // never leave while a save is half way
    if (dirty) setMode('leave')
    else onClose()
  }
  return (
    <Drawer open title={`Ficha de ${person.name}`} onClose={requestClose} size="lg">
      <div className="px-5">
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
          <div className="pb-8">
            <InvitationBody person={person} readiness={readiness} sectors={sectors} can={can} busy={busy} actions={props} />
          </div>
        ) : (
          <>
            {person.state === 'active' && sectors && (
              <ReadinessBox
                person={person}
                readiness={readiness}
                canManage={can.manageAgents}
                busy={saving}
                pendingOn={edits.attends === true && person.agent?.status !== 'active'}
                onAttend={(on) => edit({ ...edits, attends: on, fresh: on && !person.agent ? true : edits.fresh })}
              />
            )}
            <div className="mt-4 pb-8">
              <RoleSection
                person={person}
                roles={roles}
                canChange={can.manageMembers && person.state !== 'revoked'}
                busy={saving}
                value={roleOf(person, edits)}
                onChange={(roleKey) => edit({ ...edits, roleKey })}
              />
              {person.state === 'active' && sectors && (
                <AttendSection person={person} sectors={sectors} canManage={can.manageAgents} busy={saving} edits={edits} onEdit={edit} />
              )}
              {person.state === 'active' && sectors && person.agent && member && <ShiftSection userId={member.user_id} sectors={sectors} />}
              {can.manageMembers && <AccessSection person={person} busy={busy === 'status'} onSetStatus={props.onSetStatus} />}
            </div>
            {(dirty || mode === 'leave') && (
              <SaveBar
                person={person}
                sectors={sectors}
                edits={edits}
                busy={saving}
                mode={mode}
                onMode={setMode}
                onDiscard={() => edit(NO_EDITS)}
                onSave={() => void save()}
                onCloseAnyway={() => {
                  setEdits(NO_EDITS)
                  setMode('idle')
                  onClose()
                }}
              />
            )}
          </>
        )}
      </div>
    </Drawer>
  )
}
