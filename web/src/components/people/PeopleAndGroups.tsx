import { useId, useState, type ReactNode } from 'react'
import clsx from 'clsx'
import {
  Avatar,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  Icon,
  Input,
  Modal,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  Tabs,
} from '../primitives'
import type { QueueMode } from '../../lib/queues'
import {
  hasNoDefaultGroup,
  pluralize,
  type GroupRow,
  type PersonRow,
} from '../../lib/peopleModel'

export type PeopleTab = 'people' | 'groups'

type Loadable<T> = { state: 'loading' | 'error' | 'ready'; items: T[]; error?: string }

export interface PeopleAndGroupsProps {
  tab: PeopleTab
  onTabChange: (tab: PeopleTab) => void
  people: Loadable<PersonRow>
  groups: Loadable<GroupRow>
  canManage: boolean
  /** id of the item being changed: its controls are disabled while it runs. */
  busyKey?: string | null
  actionError?: string | null
  onMakeOperator: (membershipId: string) => void
  onSetOperatorStatus: (profileId: string, status: 'active' | 'disabled') => void
  onAddToGroup: (profileId: string, groupId: string) => void
  onRemoveFromGroup: (profileId: string, memberId: string) => void
  onUpdateMembership: (profileId: string, memberId: string, values: { available: boolean; capacity: number }) => void
  onCreateGroup: (values: { name: string; mode: QueueMode; isDefault: boolean }) => void
  onRenameGroup: (id: string, name: string) => void
  onChangeGroupMode: (id: string, mode: QueueMode) => void
  onMakeDefault: (id: string) => void
  onDeleteGroup: (id: string) => void
}

const MODE_LABEL: Record<QueueMode, string> = { manual: 'Manual', round_robin: 'Rodízio' }
const MODE_HELP: Record<QueueMode, string> = {
  manual: 'As conversas ficam no grupo para os operadores assumirem.',
  round_robin: 'O sistema distribui as conversas entre os operadores disponíveis.',
}
const NAME_MAX = 60
const CAPACITY_MIN = 1
const CAPACITY_MAX = 100

// "" or garbage -> 1; anything above the limit -> 100; fractions are rounded.
export function clampCapacity(raw: string): number {
  const n = Math.round(Number(raw))
  if (!Number.isFinite(n) || n < CAPACITY_MIN) return CAPACITY_MIN
  return Math.min(CAPACITY_MAX, n)
}

function Switch({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={clsx(
        'relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary focus-visible:ring-offset-2',
        'disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'border-accent-primary bg-accent-primary' : 'border-border-light bg-surface-muted',
      )}
    >
      <span className={clsx('inline-block h-4 w-4 rounded-full bg-white shadow transition-transform', checked ? 'translate-x-6' : 'translate-x-1')} />
    </button>
  )
}

function Check({ checked, onChange, label, disabled, children }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean; children: ReactNode }) {
  return (
    <label className="inline-flex items-center gap-2 text-sm font-medium text-text-primary">
      <input
        type="checkbox"
        aria-label={label}
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="h-4 w-4 rounded-sm border-border-light accent-[var(--color-accent-primary,#2563eb)] focus-visible:ring-2 focus-visible:ring-accent-primary disabled:opacity-50"
      />
      {children}
    </label>
  )
}

function Spinner() {
  return <span className="inline-block h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent motion-reduce:animate-none" aria-hidden="true" />
}

function initialsOf(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

function PersonIdentity({ person }: { person: PersonRow }) {
  return (
    <div className="flex min-w-0 items-center gap-3">
      <Avatar alt={person.name} initials={initialsOf(person.name)} size="sm" />
      <div className="min-w-0">
        <p className="truncate font-medium text-text-primary">{person.name}</p>
        <p className="truncate text-xs text-text-secondary">{person.email}</p>
      </div>
    </div>
  )
}

function ListSkeleton() {
  return (
    <div className="space-y-4 p-6" aria-busy="true">
      <span className="sr-only">Carregando</span>
      <Skeleton width="w-full" height="h-6" count={4} />
    </div>
  )
}

// ----------------------------------------------------------------- Pessoas ---

function GroupsManagerModal({
  person,
  groups,
  onClose,
  busyKey,
  onAddToGroup,
  onRemoveFromGroup,
  onUpdateMembership,
}: {
  person: PersonRow
  groups: GroupRow[]
  onClose: () => void
  busyKey?: string | null
} & Pick<PeopleAndGroupsProps, 'onAddToGroup' | 'onRemoveFromGroup' | 'onUpdateMembership'>) {
  // Only the capacity needs a draft (it is typed, then saved); everything else
  // is saved at the moment it changes. The draft is cleared once saved so the
  // value shown goes back to what the server holds.
  const [capacityDraft, setCapacityDraft] = useState<Record<string, string>>({})
  const operator = person.operator
  if (!operator) return null

  return (
    <Modal
      open
      title={`Grupos de ${person.name}`}
      description="Defina em quais grupos a pessoa atende, se está disponível e quantas conversas aceita ao mesmo tempo."
      onClose={onClose}
      footer={<Button variant="secondary" size="sm" onClick={onClose}>Concluir</Button>}
    >
      {groups.length === 0 && <p className="text-sm text-text-secondary">Nenhum grupo ainda. Crie um na aba Grupos.</p>}
      <ul className="-my-2 divide-y divide-border-subtle">
        {groups.map((group) => {
          const membership = operator.groups.find((g) => g.groupId === group.id)
          const busy = busyKey === group.id || busyKey === membership?.memberId || busyKey === `${operator.profileId}:${group.id}`
          const typed = membership ? capacityDraft[membership.memberId] : undefined
          // What is typed stays as typed while editing (clearing the field to type a
          // new number must not snap back to 1); it is normalised on blur and on save.
          const capacity = membership ? clampCapacity(typed ?? String(membership.capacity)) : CAPACITY_MIN
          return (
            <li key={group.id} className="space-y-3 py-4">
              <div className="flex items-center justify-between gap-3">
                <span className="min-w-0 truncate text-sm font-semibold text-text-primary">{group.name}</span>
                <Check
                  label={`Participa em ${group.name}`}
                  checked={Boolean(membership)}
                  disabled={busy}
                  onChange={(on) => (on ? onAddToGroup(operator.profileId, group.id) : membership && onRemoveFromGroup(operator.profileId, membership.memberId))}
                >
                  Participa
                </Check>
              </div>
              {membership && (
                <div className="grid gap-3 rounded-control bg-surface-muted p-3 sm:grid-cols-[auto_minmax(0,1fr)_auto] sm:items-end">
                  <div className="flex items-center justify-between gap-3 sm:block">
                    <span className="text-xs font-medium text-text-secondary sm:mb-1.5 sm:block">Disponível</span>
                    <Switch
                      label={`Disponível em ${group.name}`}
                      checked={membership.available}
                      disabled={busy}
                      onChange={(available) => onUpdateMembership(operator.profileId, membership.memberId, { available, capacity: membership.capacity })}
                    />
                  </div>
                  <Input
                    label="Capacidade"
                    aria-label={`Capacidade em ${group.name}`}
                    type="number"
                    min={CAPACITY_MIN}
                    max={CAPACITY_MAX}
                    value={typed ?? String(membership.capacity)}
                    disabled={busy}
                    onChange={(e) => setCapacityDraft((d) => ({ ...d, [membership.memberId]: e.target.value }))}
                    onBlur={() => typed !== undefined && setCapacityDraft((d) => ({ ...d, [membership.memberId]: String(capacity) }))}
                  />
                  <Button
                    size="sm"
                    variant="secondary"
                    isLoading={busy}
                    disabled={busy || capacity === membership.capacity}
                    aria-label={`Salvar capacidade em ${group.name}`}
                    onClick={() => {
                      onUpdateMembership(operator.profileId, membership.memberId, { available: membership.available, capacity })
                      setCapacityDraft((d) => {
                        const { [membership.memberId]: _saved, ...rest } = d
                        return rest
                      })
                    }}
                  >
                    Salvar
                  </Button>
                </div>
              )}
            </li>
          )
        })}
      </ul>
    </Modal>
  )
}

function PeopleView(props: PeopleAndGroupsProps) {
  const { people, groups, canManage, busyKey, onMakeOperator, onSetOperatorStatus } = props
  const [managedId, setManagedId] = useState<string | null>(null)
  // Look the person up in the CURRENT data so the modal follows each refetch.
  const managed = people.items.find((p) => p.membershipId === managedId) ?? null

  if (people.state === 'loading') return <ListSkeleton />
  if (people.state === 'error') {
    return <div role="alert" className="m-6 rounded-control border border-status-danger-border bg-status-danger-soft p-3 text-sm text-status-danger">{people.error}</div>
  }
  if (people.items.length === 0) {
    return <EmptyState icon={<Icon name="contacts" />} title="Nenhuma pessoa na equipe." />
  }

  const attendance = (p: PersonRow) => {
    const busy = busyKey === p.membershipId || busyKey === p.operator?.profileId
    if (!p.operator) {
      return canManage ? (
        <Button size="sm" variant="secondary" isLoading={busy} disabled={busy} onClick={() => onMakeOperator(p.membershipId)}>
          Tornar operador
        </Button>
      ) : (
        <span className="text-text-tertiary">—</span>
      )
    }
    const active = p.operator.status === 'active'
    return (
      <div className="flex flex-wrap items-center gap-2">
        <Badge size="sm" variant={active ? 'success' : 'warning'}>{active ? 'Operador' : 'Pausado'}</Badge>
        {canManage && (
          <Button
            size="sm"
            variant="tertiary"
            isLoading={busy}
            disabled={busy}
            onClick={() => onSetOperatorStatus(p.operator!.profileId, active ? 'disabled' : 'active')}
          >
            {active ? 'Pausar' : 'Reativar'}
          </Button>
        )}
      </div>
    )
  }

  const memberships = (p: PersonRow) => {
    if (!p.operator) {
      return (
        <div>
          <span className="text-text-tertiary">—</span>
          {canManage && <p className="mt-1 text-xs text-text-tertiary">Torne operador para atribuir grupos</p>}
        </div>
      )
    }
    const active = p.operator.status === 'active'
    return (
      <div className="flex flex-wrap items-center gap-1.5">
        {p.operator.groups.map((g) => (
          <Badge key={g.memberId} size="sm" variant={g.available ? 'success' : 'warning'}>
            {g.groupName}
            {g.available ? '' : ' · indisponível'}
          </Badge>
        ))}
        {p.operator.groups.length === 0 && <span className="text-text-tertiary">—</span>}
        {canManage && (
          <Button size="sm" variant="tertiary" disabled={!active} onClick={() => setManagedId(p.membershipId)}>
            Gerenciar grupos
          </Button>
        )}
        {canManage && !active && <p className="basis-full text-xs text-text-tertiary">Reative para gerenciar grupos</p>}
      </div>
    )
  }

  return (
    <>
      <div className="hidden md:block">
        <Table className="rounded-none border-0">
          <TableHead>
            <TableRow>
              <TableHeaderCell>Pessoa</TableHeaderCell>
              <TableHeaderCell>Papel</TableHeaderCell>
              <TableHeaderCell>Atendimento</TableHeaderCell>
              <TableHeaderCell>Grupos</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {people.items.map((p) => (
              <TableRow key={p.membershipId}>
                <TableCell><PersonIdentity person={p} /></TableCell>
                <TableCell><Badge size="sm">{p.roleName}</Badge></TableCell>
                <TableCell>{attendance(p)}</TableCell>
                <TableCell className="max-w-sm">{memberships(p)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <ul className="divide-y divide-border-subtle md:hidden">
        {people.items.map((p) => (
          <li key={p.membershipId} className="space-y-4 p-4">
            <div className="flex items-start justify-between gap-3">
              <PersonIdentity person={p} />
              <Badge size="sm">{p.roleName}</Badge>
            </div>
            <div>
              <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-text-tertiary">Atendimento</p>
              {attendance(p)}
            </div>
            <div>
              <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-text-tertiary">Grupos</p>
              {memberships(p)}
            </div>
          </li>
        ))}
      </ul>

      {managed && (
        <GroupsManagerModal
          person={managed}
          groups={groups.state === 'ready' ? groups.items : []}
          onClose={() => setManagedId(null)}
          busyKey={busyKey}
          onAddToGroup={props.onAddToGroup}
          onRemoveFromGroup={props.onRemoveFromGroup}
          onUpdateMembership={props.onUpdateMembership}
        />
      )}
    </>
  )
}

// ------------------------------------------------------------------ Grupos ---

function NewGroupModal({ busy, onCancel, onCreate }: { busy: boolean; onCancel: () => void; onCreate: PeopleAndGroupsProps['onCreateGroup'] }) {
  const [name, setName] = useState('')
  const [mode, setMode] = useState<QueueMode>('manual')
  const [isDefault, setIsDefault] = useState(false)
  const radioName = useId()
  const trimmed = name.trim()
  return (
    <Modal
      open
      title="Novo grupo"
      description="Um grupo é uma fila: é nele que as conversas esperam e de onde são distribuídas."
      onClose={onCancel}
      footer={
        <>
          <Button variant="secondary" size="sm" onClick={onCancel} disabled={busy}>Cancelar</Button>
          <Button size="sm" isLoading={busy} disabled={!trimmed || busy} onClick={() => onCreate({ name: trimmed, mode, isDefault })}>
            Criar grupo
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        <Input label="Nome" maxLength={NAME_MAX} value={name} onChange={(e) => setName(e.target.value)} />
        <fieldset>
          <legend className="mb-2 text-sm font-medium text-text-primary">Modo</legend>
          <div className="flex flex-wrap gap-4">
            {(['manual', 'round_robin'] as const).map((m) => (
              <label key={m} className="inline-flex items-center gap-2 text-sm text-text-primary">
                <input type="radio" name={radioName} checked={mode === m} onChange={() => setMode(m)} className="h-4 w-4 accent-[var(--color-accent-primary,#2563eb)]" />
                {MODE_LABEL[m]}
              </label>
            ))}
          </div>
          <p className="mt-2 text-xs text-text-secondary">{MODE_HELP[mode]}</p>
        </fieldset>
        <Check label="Usar como grupo padrão" checked={isDefault} onChange={setIsDefault}>Usar como grupo padrão</Check>
      </div>
    </Modal>
  )
}

function GroupCard({ group, canManage, busy, props }: { group: GroupRow; canManage: boolean; busy: boolean; props: PeopleAndGroupsProps }) {
  const [renaming, setRenaming] = useState(false)
  const [draft, setDraft] = useState(group.name)
  const [confirmDelete, setConfirmDelete] = useState(false)
  return (
    <li className="rounded-card border border-border-subtle bg-surface p-4 shadow-sm">
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-center">
        <div className="min-w-0">
          {renaming ? (
            <form
              className="flex max-w-md flex-wrap items-end gap-2"
              onSubmit={(e) => {
                e.preventDefault()
                if (draft.trim()) {
                  props.onRenameGroup(group.id, draft.trim())
                  setRenaming(false)
                }
              }}
            >
              <div className="min-w-40 flex-1">
                <Input aria-label={`Novo nome de ${group.name}`} maxLength={NAME_MAX} value={draft} onChange={(e) => setDraft(e.target.value)} />
              </div>
              <Button size="sm" type="submit" disabled={!draft.trim() || busy}>Salvar</Button>
              <Button size="sm" variant="tertiary" type="button" onClick={() => setRenaming(false)}>Cancelar</Button>
            </form>
          ) : (
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <h3 className="min-w-0 truncate font-semibold text-text-primary">{group.name}</h3>
              {group.isDefault && <Badge size="sm" variant="info">Padrão</Badge>}
              <Badge size="sm">{MODE_LABEL[group.mode]}</Badge>
            </div>
          )}
          <p className="mt-2 text-sm text-text-secondary">
            {pluralize(group.memberCount, 'pessoa', 'pessoas')} · {pluralize(group.availableCount, 'disponível', 'disponíveis')} ·{' '}
            {pluralize(group.openConversationCount, 'conversa aberta', 'conversas abertas')}
          </p>
          <p className="mt-1 text-xs text-text-tertiary">{MODE_HELP[group.mode]}</p>
        </div>

        {canManage && (
          <div className="flex flex-wrap items-center gap-2">
            {!group.isDefault && (
              <Button size="sm" variant="secondary" disabled={busy} isLoading={busy} onClick={() => props.onMakeDefault(group.id)}>
                Definir como padrão
              </Button>
            )}
            <Button size="sm" variant="tertiary" disabled={busy} onClick={() => { setDraft(group.name); setRenaming(true) }}>
              Renomear
            </Button>
            <select
              aria-label={`Modo de ${group.name}`}
              value={group.mode}
              disabled={busy}
              onChange={(e) => props.onChangeGroupMode(group.id, e.target.value as QueueMode)}
              className="h-9 rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary disabled:opacity-50"
            >
              <option value="manual">Manual</option>
              <option value="round_robin">Rodízio</option>
            </select>
            <div>
              <Button size="sm" variant="tertiary" className="text-status-danger" disabled={group.isDefault || busy} onClick={() => setConfirmDelete(true)}>
                Excluir
              </Button>
              {group.isDefault && <p className="mt-1 max-w-52 text-xs text-text-tertiary">Defina outro grupo como padrão antes de excluir</p>}
            </div>
          </div>
        )}
      </div>
      <ConfirmDialog
        open={confirmDelete}
        title={`Excluir o grupo ${group.name}?`}
        message="As pessoas deixam de fazer parte dele. Esta ação não pode ser desfeita."
        confirmLabel="Excluir"
        destructive
        isPending={busy}
        onConfirm={() => { props.onDeleteGroup(group.id); setConfirmDelete(false) }}
        onCancel={() => setConfirmDelete(false)}
      />
    </li>
  )
}

function GroupsView(props: PeopleAndGroupsProps) {
  const { groups, canManage, busyKey } = props
  const [creating, setCreating] = useState(false)
  if (groups.state === 'loading') return <ListSkeleton />
  if (groups.state === 'error') {
    return <div role="alert" className="m-6 rounded-control border border-status-danger-border bg-status-danger-soft p-3 text-sm text-status-danger">{groups.error}</div>
  }
  return (
    <div className="space-y-4 p-4 sm:p-6">
      {canManage && (
        <div className="flex justify-end">
          <Button onClick={() => setCreating(true)}>Novo grupo</Button>
        </div>
      )}
      {groups.items.length === 0 ? (
        <EmptyState icon={<Icon name="conversations" />} title="Nenhum grupo ainda. Crie o primeiro para começar a distribuir conversas." />
      ) : (
        <ul className="grid gap-3">
          {groups.items.map((g) => (
            <GroupCard key={g.id} group={g} canManage={canManage} busy={busyKey === g.id} props={props} />
          ))}
        </ul>
      )}
      {creating && (
        <NewGroupModal
          busy={busyKey === 'new-group'}
          onCancel={() => setCreating(false)}
          onCreate={(values) => { props.onCreateGroup(values); setCreating(false) }}
        />
      )}
    </div>
  )
}

// ------------------------------------------------------------------- Tela ---

export function PeopleAndGroups(props: PeopleAndGroupsProps) {
  const noDefault = props.groups.state === 'ready' && hasNoDefaultGroup(props.groups.items)
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-section-lg font-semibold text-text-primary">Pessoas e grupos</h1>
        <p className="mt-1 text-text-secondary">Escolha quem atende e organize os grupos de atendimento.</p>
      </div>

      {noDefault && (
        <div role="status" className="flex flex-wrap items-center justify-between gap-3 rounded-card border border-status-warning-border bg-status-warning-soft p-4">
          <p className="min-w-0 flex-1 text-sm font-medium text-text-primary">
            Nenhum grupo é o padrão: as conversas novas não estão sendo distribuídas. Escolha um grupo padrão na aba Grupos.
          </p>
          <Button size="sm" variant="secondary" onClick={() => props.onTabChange('groups')}>Ir para Grupos</Button>
        </div>
      )}
      {props.actionError && (
        <div role="alert" className="rounded-control border border-status-danger-border bg-status-danger-soft p-3 text-sm text-status-danger">{props.actionError}</div>
      )}
      {!props.canManage && <p className="rounded-control bg-surface-muted px-3 py-2 text-sm text-text-secondary">Você pode ver, mas não alterar.</p>}

      <section className="rounded-card border border-border-subtle bg-surface">
        <Tabs<PeopleTab>
          aria-label="Pessoas e grupos"
          items={[{ id: 'people', label: 'Pessoas' }, { id: 'groups', label: 'Grupos' }]}
          value={props.tab}
          onChange={props.onTabChange}
          className="px-2"
        />
        <div role="tabpanel" aria-label={props.tab === 'people' ? 'Pessoas' : 'Grupos'}>
          {props.tab === 'people' ? <PeopleView {...props} /> : <GroupsView {...props} />}
        </div>
      </section>
    </div>
  )
}

