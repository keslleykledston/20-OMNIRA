import { useId, useState } from 'react'
import { Button, Input, Modal } from '../primitives'
import type { QueueMode } from '../../lib/queues'
import type { Sector } from '../../lib/sectors'
import { MODE_HELP, MODE_LABEL } from './queueText'

export interface NewQueueValues {
  name: string
  mode: QueueMode
  isDefault: boolean
  sectorId: string | null
}

export function NewQueueDialog({ sectors, busy, onCancel, onCreate }: {
  sectors: Sector[]
  busy: boolean
  onCancel: () => void
  onCreate: (values: NewQueueValues) => void
}) {
  const [name, setName] = useState('')
  const [mode, setMode] = useState<QueueMode>('manual')
  const [isDefault, setIsDefault] = useState(false)
  const [sectorId, setSectorId] = useState('')
  const radioName = useId()
  const trimmed = name.trim()
  return (
    <Modal
      open
      title="Nova fila"
      description="A fila é onde as conversas esperam e de onde são distribuídas para quem atende."
      onClose={onCancel}
      footer={
        <>
          <Button variant="secondary" size="sm" onClick={onCancel} disabled={busy}>Cancelar</Button>
          <Button size="sm" isLoading={busy} disabled={!trimmed || busy} onClick={() => onCreate({ name: trimmed, mode, isDefault, sectorId: sectorId || null })}>
            Criar fila
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        <Input label="Nome da fila" maxLength={60} value={name} onChange={(e) => setName(e.target.value)} />
        <fieldset>
          <legend className="mb-2 text-sm font-medium text-text-primary">Estratégia</legend>
          <div className="flex flex-wrap gap-4">
            {(['manual', 'round_robin', 'least_loaded'] as const).map((m) => (
              <label key={m} className="inline-flex items-center gap-2 text-sm text-text-primary">
                <input type="radio" name={radioName} checked={mode === m} onChange={() => setMode(m)} className="h-4 w-4 accent-[var(--color-accent-primary)]" />
                {MODE_LABEL[m]}
              </label>
            ))}
          </div>
          <p className="mt-2 text-xs text-text-secondary">{MODE_HELP[mode]}</p>
        </fieldset>
        {sectors.length > 0 && (
          <label className="block">
            <span className="mb-1 block text-sm font-medium text-text-primary">Setor</span>
            <select
              value={sectorId}
              onChange={(e) => setSectorId(e.target.value)}
              className="h-10 w-full rounded-control border border-border-light bg-surface px-3 text-sm text-text-primary"
            >
              <option value="">Sem setor</option>
              {sectors.filter((s) => s.active).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          </label>
        )}
        <label className="inline-flex items-center gap-2 text-sm font-medium text-text-primary">
          <input type="checkbox" checked={isDefault} onChange={(e) => setIsDefault(e.target.checked)} className="h-4 w-4 accent-[var(--color-accent-primary)]" />
          Usar como fila padrão (recebe as conversas novas sem regra de canal)
        </label>
      </div>
    </Modal>
  )
}
