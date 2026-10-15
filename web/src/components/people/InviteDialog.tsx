import { useState } from 'react'
import { Button, Input, Modal } from '../primitives'
import type { RoleOption } from '../../lib/team'
import type { AttendancePlan } from '../../lib/invitations'
import type { PersonEntry } from '../../lib/peopleModel'
import { RoleOptions, type SheetSector } from './PersonSheet'
import { CapacityStepper, Switch } from './controls'

const EMAIL = /^\S+@\S+\.\S+$/

export function InviteDialog({ roles, people, sectors, canPlan, isSaving, onCancel, onConfirm, onOpenPerson }: {
  roles: RoleOption[]
  people: PersonEntry[]
  /** Queues by sector; null when they cannot be read here. */
  sectors: SheetSector[] | null
  /** agent.manage: choosing who attends (ADR-0046) goes with the invitation. */
  canPlan: boolean
  isSaving: boolean
  onCancel: () => void
  onConfirm: (email: string, roleKey: string, plan?: AttendancePlan) => void
  onOpenPerson: (key: string) => void
}) {
  const [email, setEmail] = useState('')
  const [touched, setTouched] = useState(false)
  const [roleKey, setRoleKey] = useState(() => (roles.some((r) => r.key === 'tenant_agent') ? 'tenant_agent' : roles[0]?.key ?? 'tenant_agent'))
  // Agents attend by default; admins and supervisors only when chosen. Once the switch is touched, the choice stays.
  const [attendsChoice, setAttendsChoice] = useState<boolean | null>(null)
  const attends = attendsChoice ?? roleKey === 'tenant_agent'
  const [queueIds, setQueueIds] = useState<string[]>([])
  const [capacity, setCapacity] = useState(1)

  const normalized = email.trim().toLowerCase()
  const valid = EMAIL.test(normalized)
  const same = (p: PersonEntry) => p.email.toLowerCase() === normalized
  const member = valid ? people.find((p) => same(p) && (p.state === 'active' || p.state === 'inactive')) : undefined
  const pending = valid && !member ? people.find((p) => same(p) && (p.state === 'invited' || p.state === 'invite_expired')) : undefined
  const planning = canPlan && sectors !== null
  const anyQueue = (sectors ?? []).some((s) => s.queues.length > 0)
  const toggleQueue = (id: string, on: boolean) => setQueueIds((ids) => (on ? [...ids, id] : ids.filter((x) => x !== id)))

  return (
    <Modal
      open
      title="Adicionar pessoa"
      description="A pessoa recebe um convite por e-mail para entrar nesta instância."
      onClose={onCancel}
      footer={
        <>
          <Button variant="tertiary" onClick={onCancel} disabled={isSaving}>Cancelar</Button>
          <Button
            variant="primary"
            disabled={isSaving || !!member}
            onClick={() => {
              setTouched(true)
              if (!valid || member) return
              onConfirm(normalized, roleKey, planning && attends ? { attends: true, queue_ids: queueIds, queue_capacity: capacity } : undefined)
            }}
          >
            {isSaving ? 'Enviando…' : 'Enviar convite'}
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        <Input
          id="invite-email"
          label="E-mail"
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          onBlur={() => setTouched(true)}
          error={touched && !valid ? 'Informe um e-mail válido.' : undefined}
          autoComplete="off"
        />
        {member && (
          <div role="status" className="flex flex-wrap items-center justify-between gap-2 rounded-control border border-status-warning-border bg-status-warning-soft px-3 py-2 text-sm text-text-primary">
            <span>{member.name} já faz parte desta instância{member.state === 'inactive' ? ' (acesso suspenso)' : ''}.</span>
            <Button size="sm" variant="secondary" onClick={() => onOpenPerson(member.key)}>Abrir a ficha</Button>
          </div>
        )}
        {pending && (
          <p role="status" className="rounded-control bg-status-info-soft px-3 py-2 text-sm text-text-primary">
            Já existe um convite para este e-mail. Enviar de novo substitui o anterior.
          </p>
        )}
        <fieldset>
          <legend className="mb-2 text-sm font-medium text-text-primary">Função</legend>
          <RoleOptions roles={roles} name="invite-role" value={roleKey} onChange={setRoleKey} disabled={isSaving} />
        </fieldset>

        {planning ? (
          <fieldset className="space-y-3 rounded-control border border-border-subtle p-3">
            <legend className="px-1 text-sm font-medium text-text-primary">Atendimento</legend>
            <div className="flex items-center justify-between gap-4">
              <span className="text-sm text-text-primary">Vai atender conversas</span>
              <Switch checked={attends} onChange={setAttendsChoice} label="Vai atender conversas" disabled={isSaving} />
            </div>
            {attends && (
              <>
                {anyQueue ? (
                  (sectors ?? []).filter((s) => s.queues.length > 0).map((s) => (
                    <div key={s.id ?? 'sem-setor'}>
                      <p className="mb-1 text-xs font-semibold uppercase tracking-wide text-text-tertiary">{s.name}</p>
                      <div className="flex flex-wrap gap-x-5 gap-y-2">
                        {s.queues.map((q) => (
                          <label key={q.id} className="inline-flex items-center gap-2 text-sm text-text-primary">
                            <input
                              type="checkbox"
                              checked={queueIds.includes(q.id)}
                              disabled={isSaving}
                              onChange={(e) => toggleQueue(q.id, e.target.checked)}
                              aria-label={`Recebe conversas da fila ${q.name}`}
                              className="h-4 w-4 accent-[var(--color-accent-primary)]"
                            />
                            {q.name}
                          </label>
                        ))}
                      </div>
                    </div>
                  ))
                ) : (
                  <p className="text-xs text-text-secondary">Nenhuma fila criada ainda.</p>
                )}
                {queueIds.length === 0 && (
                  <p className="text-xs text-text-tertiary">Sem fila escolhida, a pessoa entra na fila padrão de transbordo da empresa, se houver uma.</p>
                )}
                <div className="flex flex-wrap items-center gap-2 text-sm text-text-secondary">
                  Até
                  <CapacityStepper value={capacity} onChange={setCapacity} label="Conversas ao mesmo tempo em cada fila" disabled={isSaving} />
                  conversas ao mesmo tempo em cada fila
                </div>
              </>
            )}
            <p className="text-xs text-text-tertiary">Vale assim que a pessoa aceitar o convite; dá para mudar depois na ficha dela.</p>
          </fieldset>
        ) : (
          <p className="text-xs text-text-tertiary">Depois do aceite, abra a pessoa nesta lista, ligue “Recebe conversas” e escolha as filas.</p>
        )}
      </div>
    </Modal>
  )
}
