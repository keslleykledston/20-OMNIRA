import { useState } from 'react'
import clsx from 'clsx'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Button, ConfirmDialog, Modal } from '../../primitives'
import { groupAiAPI, groupAiErrorMessage } from '../../../lib/groupAi'
import { showToast } from '../../../lib/toast'
import { groupAiKey } from '../../../lib/useGroupAi'
import { Notice, SectionTitle, fieldClass } from './ui'

interface Props {
  groupId: string
  groupName: string
  aiEnabled: boolean
  canManage: boolean
  canErase: boolean
  /** The imports list is only known to exist when its route answered; without it the "discard the files too" question is not asked. */
  importsAvailable: boolean
}

export const ERASE_WORD = 'APAGAR'

// The opt-in of the group, the privacy note and the danger zone. Turning the AI on is a privacy decision (excerpts go to the AI provider),
// so it is confirmed; turning it off asks whether the imported raw files go too.
export function SettingsTab({ groupId, groupName, aiEnabled, canManage, canErase, importsAvailable }: Props) {
  const qc = useQueryClient()
  const [error, setError] = useState<string | null>(null)
  const [enabling, setEnabling] = useState(false)
  const [disabling, setDisabling] = useState(false)
  const [discardImports, setDiscardImports] = useState(false)
  const [erasing, setErasing] = useState(false)
  const [typed, setTyped] = useState('')

  const refresh = () => void qc.invalidateQueries({ queryKey: groupAiKey(groupId) })

  const toggle = useMutation({
    mutationFn: ({ enabled, discard }: { enabled: boolean; discard: boolean }) => groupAiAPI.setAI(groupId, enabled, discard),
    onSuccess: (r) => {
      setError(null)
      setEnabling(false)
      setDisabling(false)
      setDiscardImports(false)
      refresh()
      showToast(
        r.ai_enabled
          ? 'IA ligada neste grupo.'
          : r.imports_discarded > 0
            ? `IA desligada. ${r.imports_discarded} ${r.imports_discarded === 1 ? 'arquivo original descartado' : 'arquivos originais descartados'}.`
            : 'IA desligada neste grupo. A memória continua disponível.',
        'info',
      )
    },
    onError: (err) => { setEnabling(false); setDisabling(false); setError(groupAiErrorMessage(err, 'toggle')); refresh() },
  })

  const erase = useMutation({
    mutationFn: () => groupAiAPI.eraseMemory(groupId),
    onSuccess: (r) => {
      setError(null)
      setErasing(false)
      setTyped('')
      refresh()
      showToast(`Memória apagada: ${r.items_deleted} ${r.items_deleted === 1 ? 'item' : 'itens'} e ${r.imports_discarded} ${r.imports_discarded === 1 ? 'arquivo original descartado' : 'arquivos originais descartados'}.`)
    },
    onError: (err) => { setErasing(false); setError(groupAiErrorMessage(err, 'erase')); refresh() },
  })

  return (
    <div className="space-y-5 p-4">
      <section aria-label="IA neste grupo" className="space-y-2">
        <SectionTitle>IA neste grupo</SectionTitle>
        <div className="flex items-start justify-between gap-3 rounded-card border border-border-subtle bg-surface p-3">
          <div className="min-w-0">
            <p className="text-sm font-medium text-text-primary">{aiEnabled ? 'A IA está ligada' : 'A IA está desligada'}</p>
            <p className="mt-1 text-xs leading-relaxed text-text-secondary">
              Ao ligar, trechos das mensagens deste grupo são enviados ao provedor de IA para montar a memória. Ela só propõe: nada é enviado ao grupo.
            </p>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={aiEnabled}
            aria-label="IA neste grupo"
            disabled={!canManage || toggle.isPending}
            onClick={() => (aiEnabled ? setDisabling(true) : setEnabling(true))}
            className={clsx(
              'relative mt-0.5 h-6 w-11 flex-shrink-0 rounded-full transition-colors focus-visible:ring-2 focus-visible:ring-accent-primary focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50',
              aiEnabled ? 'bg-accent-primary' : 'bg-border-light',
            )}
          >
            <span className={clsx('absolute left-0.5 top-0.5 h-5 w-5 rounded-full bg-white shadow-sm transition-transform', aiEnabled && 'translate-x-5')} />
          </button>
        </div>
        {!canManage && <p className="text-xs text-text-tertiary">Só quem tem permissão para gerenciar o copiloto pode ligar ou desligar a IA.</p>}
        {error && <Notice tone="danger" role="alert">{error}</Notice>}
      </section>

      <section aria-label="Privacidade" className="space-y-2">
        <SectionTitle>Privacidade</SectionTitle>
        <ul className="list-disc space-y-1.5 pl-4 text-xs leading-relaxed text-text-secondary">
          <li>A memória é um resumo derivado das mensagens. Pode conter dados pessoais de quem participa do grupo, e pode ser editada, corrigida ou apagada por quem tem permissão.</li>
          <li>Cada item mostra de onde veio e se uma pessoa o confirmou. O que a IA inferiu aparece marcado como inferido.</li>
          <li>Um arquivo importado fica guardado cifrado, por padrão por 90 dias, e depois é descartado. A tela nunca o mostra nem permite baixá-lo.</li>
          <li>Apagar o histórico do grupo remove os trechos literais e os arquivos importados, mas mantém os itens resumidos. Para tirá-los também, use &quot;Apagar toda a memória do grupo&quot;.</li>
        </ul>
      </section>

      {canErase && (
        <section aria-label="Zona de perigo" className="space-y-2 rounded-card border border-status-danger-border bg-status-danger-soft p-3">
          <SectionTitle>Zona de perigo</SectionTitle>
          <p className="text-xs leading-relaxed text-text-primary">
            Apaga todos os itens da memória deste grupo, zera a leitura da IA e descarta os arquivos originais importados. As mensagens do grupo não são apagadas. Isto não pode ser desfeito.
          </p>
          <Button variant="danger" size="sm" onClick={() => setErasing(true)}>
            Apagar toda a memória do grupo
          </Button>
        </section>
      )}

      <ConfirmDialog
        open={enabling}
        title="Ligar a IA neste grupo?"
        message="Trechos das mensagens deste grupo serão enviados ao provedor de IA para montar a memória. Você pode desligar quando quiser."
        confirmLabel="Ligar a IA"
        isPending={toggle.isPending}
        onConfirm={() => toggle.mutate({ enabled: true, discard: false })}
        onCancel={() => setEnabling(false)}
      />

      <Modal
        open={disabling}
        title="Desligar a IA neste grupo?"
        size="sm"
        onClose={() => { setDisabling(false); setDiscardImports(false) }}
        footer={
          <>
            <Button variant="secondary" size="sm" onClick={() => { setDisabling(false); setDiscardImports(false) }} disabled={toggle.isPending}>
              Cancelar
            </Button>
            <Button size="sm" isLoading={toggle.isPending} onClick={() => toggle.mutate({ enabled: false, discard: discardImports })}>
              Desligar a IA
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <p className="text-sm text-text-secondary">A IA para de ler este grupo. A memória continua disponível para consulta e pode ser apagada em Ajustes.</p>
          {importsAvailable && (
            <label className="flex items-start gap-2 text-sm text-text-primary">
              <input type="checkbox" checked={discardImports} onChange={(e) => setDiscardImports(e.target.checked)} className="mt-0.5" />
              <span>Descartar também os arquivos originais importados (não poderão ser reprocessados)</span>
            </label>
          )}
        </div>
      </Modal>

      <Modal
        open={erasing}
        title="Apagar toda a memória do grupo?"
        size="sm"
        onClose={() => { setErasing(false); setTyped('') }}
        footer={
          <>
            <Button variant="secondary" size="sm" onClick={() => { setErasing(false); setTyped('') }} disabled={erase.isPending}>
              Cancelar
            </Button>
            <Button variant="danger" size="sm" disabled={typed !== ERASE_WORD} isLoading={erase.isPending} onClick={() => erase.mutate()}>
              Apagar toda a memória
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <p className="text-sm text-text-secondary">
            Todos os itens da memória de &quot;{groupName || 'este grupo'}&quot; serão apagados, a leitura da IA será zerada e os arquivos originais importados serão descartados. Não dá para desfazer.
          </p>
          <label className="block text-sm text-text-primary">
            Para confirmar, digite {ERASE_WORD}
            <input aria-label={`Digite ${ERASE_WORD} para confirmar`} value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" className={`${fieldClass} mt-1`} />
          </label>
        </div>
      </Modal>
    </div>
  )
}
