import { useState } from 'react'
import { TextArea } from '../primitives'
import { DEFAULT_EXIT_COMMANDS, type CustomerExit } from '../../lib/flows'

interface Props {
  value?: CustomerExit
  readOnly?: boolean
  onChange: (next: CustomerExit | undefined) => void
}

const inputCls = 'w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary'

/**
 * Flow-level setting: the contact can end the attendance by typing a command. The safeguards are fixed on purpose (whole
 * message only, always a yes/no confirmation) and are explained here so nobody expects a looser behaviour.
 */
export default function CustomerExitSettings({ value, readOnly, onChange }: Props) {
  const enabled = !!value?.enabled
  const [commandsText, setCommandsText] = useState((value?.commands ?? []).join(', '))
  const emit = (patch: Partial<CustomerExit>) => onChange({ enabled, ...value, ...patch })

  return (
    <fieldset className="mt-4 space-y-3 border-t border-border-subtle pt-4">
      <legend className="text-sm font-semibold text-text-primary">Cliente encerra o atendimento</legend>
      <label className="flex items-start gap-2 text-sm text-text-primary">
        <input type="checkbox" className="mt-0.5" checked={enabled} disabled={readOnly} onChange={(e) => emit({ enabled: e.target.checked })} />
        <span>Permitir que o cliente encerre digitando um comando</span>
      </label>
      {enabled && (
        <>
          <label className="block text-xs font-medium text-text-secondary">
            Comandos (separados por vírgula)
            <input
              className={`${inputCls} mt-1`}
              value={commandsText}
              disabled={readOnly}
              placeholder={DEFAULT_EXIT_COMMANDS.join(', ')}
              onChange={(e) => {
                setCommandsText(e.target.value)
                const list = e.target.value.split(',').map((c) => c.trim()).filter(Boolean)
                emit({ commands: list.length ? list : undefined })
              }}
            />
            <span className="mt-1 block font-normal text-text-tertiary">Vazio usa: {DEFAULT_EXIT_COMMANDS.join(', ')}.</span>
          </label>
          <TextArea
            label="Mensagem de despedida (opcional)"
            rows={2}
            maxLength={500}
            value={value?.farewell ?? ''}
            disabled={readOnly}
            placeholder="Atendimento encerrado a seu pedido. Se precisar de algo, é só nos escrever por aqui. Obrigado!"
            onChange={(e) => emit({ farewell: e.target.value || undefined })}
          />
          <ul className="list-disc space-y-1 pl-4 text-xs text-text-secondary">
            <li>Só vale se a mensagem inteira for o comando (curto, até 3 palavras). Frases que apenas contêm a palavra não contam.</li>
            <li>O bot sempre pede confirmação ("Sim, encerrar" ou "Não, continuar"). Só um "sim" encerra; qualquer outra resposta mantém o atendimento.</li>
            <li>Uma opção do menu atual com o mesmo texto tem prioridade sobre o comando.</li>
            <li>Vale enquanto o bot conversa e enquanto o cliente espera na fila sem ninguém ter assumido.</li>
          </ul>
        </>
      )}
    </fieldset>
  )
}
