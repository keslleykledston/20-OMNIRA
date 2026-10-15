import { useState } from 'react'
import { Button, Input, Modal } from '../primitives'
import type { RoleOption } from '../../lib/team'
import type { PersonEntry } from '../../lib/peopleModel'
import { RoleOptions } from './PersonSheet'

const EMAIL = /^\S+@\S+\.\S+$/

export function InviteDialog({ roles, people, isSaving, onCancel, onConfirm, onOpenPerson }: {
  roles: RoleOption[]
  people: PersonEntry[]
  isSaving: boolean
  onCancel: () => void
  onConfirm: (email: string, roleKey: string) => void
  onOpenPerson: (key: string) => void
}) {
  const [email, setEmail] = useState('')
  const [touched, setTouched] = useState(false)
  const [roleKey, setRoleKey] = useState(() => (roles.some((r) => r.key === 'tenant_agent') ? 'tenant_agent' : roles[0]?.key ?? 'tenant_agent'))
  const normalized = email.trim().toLowerCase()
  const valid = EMAIL.test(normalized)
  const same = (p: PersonEntry) => p.email.toLowerCase() === normalized
  const member = valid ? people.find((p) => same(p) && (p.state === 'active' || p.state === 'inactive')) : undefined
  const pending = valid && !member ? people.find((p) => same(p) && (p.state === 'invited' || p.state === 'invite_expired')) : undefined

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
              if (valid && !member) onConfirm(normalized, roleKey)
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
        <p className="text-xs text-text-tertiary">Depois do aceite, abra a pessoa nesta lista, ligue “Recebe conversas” e escolha as filas.</p>
      </div>
    </Modal>
  )
}
