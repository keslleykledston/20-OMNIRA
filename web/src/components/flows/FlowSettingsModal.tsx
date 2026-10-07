import { useEffect, useState } from 'react'
import { Button, Input, Modal } from '../primitives'
import type { Flow, RestartPolicy } from '../../lib/flows'
import type { ChannelLine } from '../../lib/channelLines'

interface Props {
  open: boolean
  flow: Flow
  lines: ChannelLine[]
  saving?: boolean
  error?: string | null
  onClose: () => void
  onSave: (v: { priority: number; is_default: boolean; restart_policy: RestartPolicy; connection_ids: string[]; providers: string[] }) => void
}

// Quando e onde o fluxo é usado: prioridade, fluxo padrão, política de reinício e as linhas de canal (WAHA e Meta convivem).
export default function FlowSettingsModal({ open, flow, lines, saving, error, onClose, onSave }: Props) {
  const [priority, setPriority] = useState(flow.priority)
  const [isDefault, setDefault] = useState(flow.is_default)
  const [policy, setPolicy] = useState<RestartPolicy>(flow.restart_policy)
  const [connections, setConnections] = useState<string[]>(flow.trigger_filter.connection_ids ?? [])
  const [providers, setProviders] = useState<string[]>(flow.trigger_filter.providers ?? [])
  const [restricted, setRestricted] = useState((flow.trigger_filter.connection_ids ?? []).length > 0)

  useEffect(() => {
    if (!open) return
    setPriority(flow.priority)
    setDefault(flow.is_default)
    setPolicy(flow.restart_policy)
    setConnections(flow.trigger_filter.connection_ids ?? [])
    setProviders(flow.trigger_filter.providers ?? [])
    setRestricted((flow.trigger_filter.connection_ids ?? []).length > 0)
  }, [open, flow])

  // "só nos números marcados" sem nenhum número marcado atenderia ninguém: não deixa salvar (e nunca vira "todos" sozinho)
  const noneChosen = restricted && connections.length === 0
  const toggle = (list: string[], v: string, set: (l: string[]) => void) => set(list.includes(v) ? list.filter((x) => x !== v) : [...list, v])
  return (
    <Modal
      open={open}
      title="Configurações do fluxo"
      description="Define quais conversas este fluxo atende e em que ordem."
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancelar</Button>
          <Button isLoading={saving} disabled={noneChosen} onClick={() => onSave({ priority, is_default: isDefault, restart_policy: policy, connection_ids: restricted ? connections : [], providers })}>Salvar</Button>
        </>
      }
    >
      <div className="space-y-4">
        <Input label="Prioridade (1 a 1000, menor roda primeiro)" type="number" min={1} max={1000} value={priority} onChange={(e) => setPriority(Number(e.target.value))} />
        <label className="flex items-center gap-2 text-sm text-text-primary">
          <input type="checkbox" checked={isDefault} onChange={(e) => setDefault(e.target.checked)} /> Fluxo padrão (atende quando nenhum outro se aplica)
        </label>
        <label className="block">
          <span className="mb-1 block text-xs font-medium text-text-secondary">Quando iniciar</span>
          <select className="w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={policy} onChange={(e) => setPolicy(e.target.value as RestartPolicy)} aria-label="Quando iniciar">
            <option value="new_conversation_only">Só em conversas novas</option>
            <option value="always">Em toda nova mensagem sem atendimento</option>
          </select>
        </label>
        <fieldset className="space-y-2">
          <legend className="text-xs font-semibold text-text-secondary">Onde este fluxo atende (número de entrada)</legend>
          <label className="flex items-center gap-2 text-sm text-text-primary">
            <input type="radio" name="flow-scope" checked={!restricted} onChange={() => { setRestricted(false); setConnections([]) }} /> Todos os números
          </label>
          <label className="flex items-center gap-2 text-sm text-text-primary">
            <input type="radio" name="flow-scope" checked={restricted} onChange={() => setRestricted(true)} disabled={lines.length === 0} /> Só nos números marcados
          </label>
          {lines.length === 0 && <p className="text-xs text-text-tertiary">Nenhuma linha de canal cadastrada.</p>}
          {restricted && (
            <div className="space-y-1 border-l border-border-subtle pl-3">
              {lines.map((l) => (
                <label key={l.id} className="flex items-center gap-2 text-sm text-text-primary">
                  <input type="checkbox" checked={connections.includes(l.id)} onChange={() => toggle(connections, l.id, setConnections)} />
                  <span>{l.label}{l.number ? ` (${l.number})` : ''}</span>
                  <span className="text-xs text-text-tertiary">{l.provider_kind === 'official' ? 'oficial' : 'não oficial'}</span>
                </label>
              ))}
              {noneChosen && <p role="alert" className="text-xs text-status-danger">Marque ao menos um número, ou escolha "Todos os números".</p>}
              <p className="text-xs text-text-tertiary">Conversas dos outros números seguem o roteamento normal, sem o bot.</p>
            </div>
          )}
        </fieldset>
        <details className="text-sm" open={providers.length > 0}>
          <summary className="cursor-pointer text-xs font-semibold text-text-secondary">Avançado: filtrar por tipo de canal</summary>
          <fieldset className="mt-2 space-y-1">
            <legend className="sr-only">Provedores (vazio = todos)</legend>
            {[['waha', 'WhatsApp (WAHA)'], ['meta_cloud', 'WhatsApp oficial (Meta)']].map(([k, l]) => (
              <label key={k} className="flex items-center gap-2 text-sm text-text-primary">
                <input type="checkbox" checked={providers.includes(k)} onChange={() => toggle(providers, k, setProviders)} /> {l}
              </label>
            ))}
          </fieldset>
        </details>
        {error && <p role="alert" className="text-sm text-status-danger">{error}</p>}
      </div>
    </Modal>
  )
}
