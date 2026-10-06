import { useState } from 'react'
import { Badge, Button, Input } from '../primitives'
import type { Scenario, SimEvent, SimulationResult } from '../../lib/flows'
import { nodeLabel } from '../../lib/flowModel'

interface Props {
  disabled?: boolean
  running?: boolean
  result: SimulationResult | null
  error?: string | null
  hasAI: boolean
  onRun: (scenario: Scenario) => void
}

const STATUS_LABEL: Record<string, string> = {
  completed: 'Concluído',
  waiting_input: 'Aguardando resposta do contato',
  waiting_human: 'Entregue a um humano',
  failed: 'Falhou',
  cancelled: 'Cancelado',
  blocked: 'Bloqueado por erros no fluxo',
  no_run: 'Não iniciou',
}

const EFFECT_LABEL: Record<string, string> = {
  ticket: 'Abriria um chamado',
  assign_queue: 'Direcionaria para uma fila',
  handoff: 'Transferiria para um humano',
  company_validated: 'Confirmaria a empresa do contato',
  return_to_queue: 'Devolveria a conversa à fila padrão',
}

export default function SimulatorPanel({ disabled, running, result, error, hasAI, onRun }: Props) {
  const [kind, setKind] = useState<'unclassified' | 'customer' | 'other'>('unclassified')
  const [provider, setProvider] = useState<'waha' | 'meta_cloud'>('waha')
  const [windowOpen, setWindowOpen] = useState(true)
  const [companies, setCompanies] = useState('')
  const [openTickets, setOpenTickets] = useState(0)
  const [events, setEvents] = useState<SimEvent[]>([{ type: 'message', text: 'oi' }])
  const [aiIntent, setAiIntent] = useState('')
  const [aiConfidence, setAiConfidence] = useState(0.9)
  const [aiFail, setAiFail] = useState(false)

  const run = () => {
    const scenario: Scenario = {
      contact: { kind },
      provider,
      window_open: windowOpen,
      companies: companies.split('\n').map((c) => c.trim()).filter(Boolean).map((name) => ({ name })),
      open_tickets: openTickets,
      events,
    }
    if (hasAI) scenario.ai = { intent: aiIntent || undefined, confidence: aiConfidence, fail: aiFail || undefined }
    onRun(scenario)
  }

  return (
    <section aria-label="Simulador" className="space-y-3">
      <header>
        <h2 className="text-sm font-semibold text-text-primary">Simulador</h2>
        <p className="text-xs text-text-secondary">Testa o fluxo com um cenário. Nada é enviado, gravado ou cobrado: é só uma simulação.</p>
      </header>
      <label className="block">
        <span className="mb-1 block text-xs font-medium text-text-secondary">Quem escreve</span>
        <select aria-label="Tipo de contato" className="w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={kind} onChange={(e) => setKind(e.target.value as typeof kind)}>
          <option value="unclassified">Contato ainda não classificado</option>
          <option value="customer">Cliente</option>
          <option value="other">Outro (não cliente)</option>
        </select>
      </label>
      <label className="block">
        <span className="mb-1 block text-xs font-medium text-text-secondary">Canal</span>
        <select aria-label="Provedor do canal" className="w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={provider} onChange={(e) => setProvider(e.target.value as typeof provider)}>
          <option value="waha">WhatsApp (WAHA)</option>
          <option value="meta_cloud">WhatsApp oficial (Meta)</option>
        </select>
      </label>
      {provider === 'meta_cloud' && (
        <label className="flex items-center gap-2 text-sm text-text-secondary">
          <input type="checkbox" checked={windowOpen} onChange={(e) => setWindowOpen(e.target.checked)} /> Janela de 24 h aberta
        </label>
      )}
      <label className="block">
        <span className="mb-1 block text-xs font-medium text-text-secondary">Empresas do contato (uma por linha)</span>
        <textarea aria-label="Empresas do contato" rows={2} className="w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={companies} onChange={(e) => setCompanies(e.target.value)} />
      </label>
      <Input label="Chamados abertos do contato" type="number" min={0} value={openTickets} onChange={(e) => setOpenTickets(Math.max(0, Number(e.target.value) || 0))} />
      <fieldset className="space-y-2 rounded-control border border-border-subtle p-3">
        <legend className="px-1 text-xs font-semibold text-text-secondary">O que acontece</legend>
        {events.map((ev, i) => (
          <div key={i} className="flex items-center gap-2">
            {ev.type === 'message' ? (
              <input aria-label={`Mensagem ${i + 1}`} className="min-w-0 flex-1 rounded-control border border-border-subtle bg-surface px-3 py-1.5 text-sm" value={ev.text ?? ''} onChange={(e) => setEvents(events.map((x, j) => (j === i ? { ...x, text: e.target.value } : x)))} />
            ) : (
              <span className="flex-1 text-sm italic text-text-secondary">O tempo passa até a espera acabar</span>
            )}
            {events.length > 1 && (
              <Button type="button" size="sm" variant="tertiary" aria-label={`Remover evento ${i + 1}`} onClick={() => setEvents(events.filter((_, j) => j !== i))}>×</Button>
            )}
          </div>
        ))}
        <div className="flex gap-2">
          <Button type="button" size="sm" variant="secondary" onClick={() => setEvents([...events, { type: 'message', text: '' }])}>Contato responde</Button>
          <Button type="button" size="sm" variant="secondary" onClick={() => setEvents([...events, { type: 'timeout' }])}>Sem resposta</Button>
        </div>
      </fieldset>
      {hasAI && (
        <fieldset className="space-y-2 rounded-control border border-border-subtle p-3">
          <legend className="px-1 text-xs font-semibold text-text-secondary">Resposta simulada da IA</legend>
          <Input label="Intenção que a IA devolve" value={aiIntent} onChange={(e) => setAiIntent(e.target.value)} helperText="Identificador de uma das intenções do nó" />
          <Input label="Confiança simulada (0 a 1)" type="number" step={0.05} min={0} max={1} value={aiConfidence} onChange={(e) => setAiConfidence(Number(e.target.value))} />
          <label className="flex items-center gap-2 text-sm text-text-secondary"><input type="checkbox" checked={aiFail} onChange={(e) => setAiFail(e.target.checked)} /> A IA falha</label>
        </fieldset>
      )}
      <Button type="button" onClick={run} isLoading={running} disabled={disabled || running}>Simular</Button>
      {error && <p role="alert" className="text-sm text-status-danger">{error}</p>}
      {result && <SimulationView result={result} />}
    </section>
  )
}

function SimulationView({ result }: { result: SimulationResult }) {
  const tone = result.status === 'completed' || result.status === 'waiting_human' ? 'success' : result.status === 'waiting_input' ? 'info' : 'danger'
  return (
    <div className="space-y-3" data-testid="simulation-result">
      <div className="flex items-center gap-2">
        <Badge variant={tone}>{STATUS_LABEL[result.status] ?? result.status}</Badge>
        {result.waiting_at && <span className="text-xs text-text-secondary">parado em: {result.waiting_at}</span>}
      </div>
      {result.error && <p className="text-xs text-status-danger">{result.error}</p>}
      {result.issues && result.issues.length > 0 && result.status === 'blocked' && (
        <ul className="m-0 list-disc space-y-1 pl-4 text-xs text-text-secondary">
          {result.issues.filter((i) => i.severity === 'error').map((i, n) => <li key={n}>{i.message}</li>)}
        </ul>
      )}
      {result.messages.length > 0 && (
        <div>
          <h3 className="mb-1 text-xs font-semibold text-text-secondary">Mensagens que o bot enviaria</h3>
          <ol className="m-0 list-none space-y-1 p-0">
            {result.messages.map((m, i) => <li key={i} className="whitespace-pre-wrap rounded-control bg-accent-primary-soft px-2 py-1 text-sm text-text-primary">{m.text}</li>)}
          </ol>
        </div>
      )}
      {result.effects.length > 0 && (
        <div>
          <h3 className="mb-1 text-xs font-semibold text-text-secondary">O que aconteceria</h3>
          <ul className="m-0 list-none space-y-1 p-0">
            {result.effects.map((e, i) => (
              <li key={i} className="text-sm text-text-primary">
                {EFFECT_LABEL[e.kind] ?? e.kind}
                {e.kind === 'ticket' && e.detail ? `: "${String(e.detail.subject ?? '')}" (prioridade ${String(e.detail.priority ?? '')})` : ''}
              </li>
            ))}
          </ul>
        </div>
      )}
      <div>
        <h3 className="mb-1 text-xs font-semibold text-text-secondary">Caminho percorrido</h3>
        <ol className="m-0 list-decimal space-y-0.5 pl-5 text-xs text-text-secondary">
          {result.steps.map((s) => (
            <li key={s.seq}>
              {nodeLabel(s.node_type)} <span className="text-text-tertiary">({s.node_id}{s.port ? ` → ${s.port}` : ''})</span>
              {s.status === 'failed' && s.error ? <span className="text-status-danger"> — {s.error}</span> : null}
            </li>
          ))}
        </ol>
      </div>
    </div>
  )
}
