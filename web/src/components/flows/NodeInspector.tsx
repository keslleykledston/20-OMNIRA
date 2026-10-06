import { useId, type ReactNode } from 'react'
import { Button, Input, TextArea } from '../primitives'
import type { Flow, FlowNode } from '../../lib/flows'
import { nodeLabel } from '../../lib/flowModel'

type Cfg = Record<string, unknown>

export interface QueueOption {
  id: string
  name: string
}

interface Props {
  node: FlowNode
  queues: QueueOption[]
  subflows: Flow[]
  variables: string[]
  readOnly?: boolean
  onConfig: (config: Cfg) => void
  onDelete: () => void
}

const selectCls = 'w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary'

function Select({ label, value, onChange, children, disabled, hint }: { label: string; value: string; onChange: (v: string) => void; children: ReactNode; disabled?: boolean; hint?: string }) {
  const id = useId()
  return (
    <div>
      <label htmlFor={id} className="mb-1 block text-xs font-medium text-text-secondary">{label}</label>
      <select id={id} className={selectCls} value={value} disabled={disabled} aria-describedby={hint ? `${id}-hint` : undefined} onChange={(e) => onChange(e.target.value)}>
        {children}
      </select>
      {hint && <span id={`${id}-hint`} className="mt-1 block text-xs text-text-tertiary">{hint}</span>}
    </div>
  )
}

function str(cfg: Cfg, key: string): string {
  const v = cfg[key]
  return typeof v === 'string' ? v : v == null ? '' : String(v)
}

function arr<T>(cfg: Cfg, key: string): T[] {
  const v = cfg[key]
  return Array.isArray(v) ? (v as T[]) : []
}

function slug(label: string): string {
  const s = label.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_+|_+$/g, '')
  return /^[a-z]/.test(s) ? s.slice(0, 40) : s ? `o_${s}`.slice(0, 40) : ''
}

// Editor de lista genérico: cada linha é um objeto pequeno; a ordem é a ordem das portas desenhadas no cartão.
function ListEditor<T extends Record<string, unknown>>({ title, items, addLabel, blank, min = 0, max = 10, disabled, onChange, row }: {
  title: string
  items: T[]
  addLabel: string
  blank: () => T
  min?: number
  max?: number
  disabled?: boolean
  onChange: (items: T[]) => void
  row: (item: T, set: (patch: Partial<T>) => void, i: number) => ReactNode
}) {
  return (
    <fieldset className="space-y-2 rounded-control border border-border-subtle p-3">
      <legend className="px-1 text-xs font-semibold text-text-secondary">{title}</legend>
      {items.map((it, i) => (
        <div key={i} className="space-y-2 rounded-control bg-surface-muted p-2">
          {row(it, (patch) => onChange(items.map((x, j) => (j === i ? { ...x, ...patch } : x))), i)}
          {items.length > min && !disabled && (
            <Button type="button" size="sm" variant="tertiary" onClick={() => onChange(items.filter((_, j) => j !== i))} aria-label={`Remover ${title.toLowerCase()} ${i + 1}`}>
              Remover
            </Button>
          )}
        </div>
      ))}
      {items.length < max && !disabled && (
        <Button type="button" size="sm" variant="secondary" onClick={() => onChange([...items, blank()])}>
          {addLabel}
        </Button>
      )}
    </fieldset>
  )
}

const WEEKDAYS: [string, string][] = [['mon', 'Seg'], ['tue', 'Ter'], ['wed', 'Qua'], ['thu', 'Qui'], ['fri', 'Sex'], ['sat', 'Sáb'], ['sun', 'Dom']]

export default function NodeInspector({ node, queues, subflows, variables, readOnly, onConfig, onDelete }: Props) {
  const cfg = (node.config ?? {}) as Cfg
  const set = (patch: Cfg) => onConfig({ ...cfg, ...patch })
  const ro = !!readOnly

  const varHint = variables.length ? `Variáveis disponíveis: ${variables.join(', ')}` : undefined
  const queueSelect = (label: string, value: string, onChange: (v: string) => void, allowEmpty: boolean) => (
    <Select label={label} value={value} disabled={ro} onChange={onChange} hint={queues.length === 0 ? 'Nenhuma fila cadastrada. Crie uma em Configurações.' : undefined}>
      <option value="">{allowEmpty ? 'Manter a fila atual' : 'Selecione uma fila'}</option>
      {queues.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
    </Select>
  )

  let form: ReactNode = null
  switch (node.type) {
    case 'send_message':
      form = <TextArea label="Texto da mensagem" value={str(cfg, 'text')} disabled={ro} rows={4} helperText={`Use {{variavel}} para inserir dados. ${varHint ?? ''}`} onChange={(e) => set({ text: e.target.value })} />
      break
    case 'ask':
      form = (
        <>
          <TextArea label="Pergunta" value={str(cfg, 'text')} disabled={ro} rows={3} onChange={(e) => set({ text: e.target.value })} />
          <Input label="Salvar a resposta na variável" value={str(cfg, 'variable')} disabled={ro} helperText="Letras minúsculas, números e _" onChange={(e) => set({ variable: e.target.value })} />
          <Select label="Validar resposta como" value={str(cfg, 'validation') || 'none'} disabled={ro} onChange={(v) => set({ validation: v === 'none' ? undefined : v })}>
            <option value="none">Texto livre</option>
            <option value="number">Número</option>
            <option value="email">E-mail</option>
            <option value="phone">Telefone</option>
          </Select>
          <Input label="Tempo de espera (segundos)" type="number" min={0} value={str(cfg, 'timeout_seconds')} disabled={ro} helperText="Vazio usa o padrão do fluxo (24 h)" onChange={(e) => set({ timeout_seconds: e.target.value === '' ? undefined : Number(e.target.value) })} />
          <Input label="Tentativas inválidas permitidas" type="number" min={0} max={10} value={str(cfg, 'max_attempts')} disabled={ro} onChange={(e) => set({ max_attempts: e.target.value === '' ? undefined : Number(e.target.value) })} />
        </>
      )
      break
    case 'choice':
      form = (
        <>
          <TextArea label="Pergunta do menu" value={str(cfg, 'text')} disabled={ro} rows={3} onChange={(e) => set({ text: e.target.value })} />
          <Input label="Salvar a escolha na variável" value={str(cfg, 'variable')} disabled={ro} onChange={(e) => set({ variable: e.target.value })} />
          <ListEditor<{ id: string; label: string; value?: string }>
            title="Opções" items={arr(cfg, 'options')} min={2} max={10} disabled={ro} addLabel="Adicionar opção"
            blank={() => ({ id: `opcao_${arr(cfg, 'options').length + 1}`, label: '' })}
            onChange={(options) => set({ options })}
            row={(o, setO, i) => (
              <>
                <Input label={`Texto da opção ${i + 1}`} value={o.label} disabled={ro} onChange={(e) => setO({ label: e.target.value, id: o.id.startsWith('opcao_') && slug(e.target.value) ? slug(e.target.value) : o.id })} />
                <Input label={`Identificador da opção ${i + 1}`} value={o.id} disabled={ro} helperText="Nome da saída no cartão" onChange={(e) => setO({ id: e.target.value })} />
                <Input label={`Valor guardado da opção ${i + 1}`} value={o.value ?? ''} disabled={ro} helperText="Vazio guarda o texto" onChange={(e) => setO({ value: e.target.value || undefined })} />
              </>
            )}
          />
        </>
      )
      break
    case 'condition':
      form = (
        <>
          <Input label="Variável" value={str(cfg, 'variable')} disabled={ro} helperText={varHint} onChange={(e) => set({ variable: e.target.value })} />
          <Select label="Operador" value={str(cfg, 'op') || 'eq'} disabled={ro} onChange={(v) => set({ op: v })}>
            <option value="eq">é igual a</option>
            <option value="neq">é diferente de</option>
            <option value="gt">é maior que</option>
            <option value="gte">é maior ou igual a</option>
            <option value="lt">é menor que</option>
            <option value="lte">é menor ou igual a</option>
            <option value="contains">contém</option>
            <option value="exists">existe</option>
            <option value="not_exists">não existe</option>
          </Select>
          {!['exists', 'not_exists'].includes(str(cfg, 'op')) && <Input label="Valor" value={str(cfg, 'value')} disabled={ro} onChange={(e) => set({ value: e.target.value })} />}
        </>
      )
      break
    case 'switch':
      form = (
        <>
          <Input label="Variável" value={str(cfg, 'variable')} disabled={ro} helperText={varHint} onChange={(e) => set({ variable: e.target.value })} />
          <ListEditor<{ id: string; op?: string; value?: unknown }>
            title="Casos" items={arr(cfg, 'cases')} min={1} max={20} disabled={ro} addLabel="Adicionar caso"
            blank={() => ({ id: `caso_${arr(cfg, 'cases').length + 1}`, value: '' })}
            onChange={(cases) => set({ cases })}
            row={(c, setC, i) => (
              <>
                <Input label={`Nome do caso ${i + 1}`} value={c.id} disabled={ro} helperText="Nome da saída no cartão" onChange={(e) => setC({ id: e.target.value })} />
                <Input label={`Valor do caso ${i + 1}`} value={String(c.value ?? '')} disabled={ro} onChange={(e) => setC({ value: e.target.value })} />
              </>
            )}
          />
        </>
      )
      break
    case 'set_variable':
      form = (
        <ListEditor<{ variable: string; value: unknown }>
          title="Atribuições" items={arr(cfg, 'assignments')} min={1} max={20} disabled={ro} addLabel="Adicionar atribuição"
          blank={() => ({ variable: '', value: '' })}
          onChange={(assignments) => set({ assignments })}
          row={(a, setA, i) => (
            <>
              <Input label={`Variável ${i + 1}`} value={a.variable} disabled={ro} onChange={(e) => setA({ variable: e.target.value })} />
              <Input label={`Valor ${i + 1}`} value={String(a.value ?? '')} disabled={ro} helperText="Aceita {{variavel}}" onChange={(e) => setA({ value: e.target.value })} />
            </>
          )}
        />
      )
      break
    case 'business_hours':
      form = (
        <>
          <Input label="Fuso horário" value={str(cfg, 'timezone')} disabled={ro} helperText="Nome IANA, ex.: America/Sao_Paulo" onChange={(e) => set({ timezone: e.target.value })} />
          <ListEditor<{ days: string[]; start: string; end: string }>
            title="Janelas de atendimento" items={arr(cfg, 'windows')} min={1} max={14} disabled={ro} addLabel="Adicionar janela"
            blank={() => ({ days: ['mon', 'tue', 'wed', 'thu', 'fri'], start: '08:00', end: '18:00' })}
            onChange={(windows) => set({ windows })}
            row={(w, setW, i) => (
              <>
                <div role="group" aria-label={`Dias da janela ${i + 1}`} className="flex flex-wrap gap-2">
                  {WEEKDAYS.map(([k, l]) => (
                    <label key={k} className="flex items-center gap-1 text-xs text-text-secondary">
                      <input type="checkbox" disabled={ro} checked={w.days.includes(k)} onChange={(e) => setW({ days: e.target.checked ? [...w.days, k] : w.days.filter((d) => d !== k) })} />
                      {l}
                    </label>
                  ))}
                </div>
                <Input label={`Início da janela ${i + 1}`} type="time" value={w.start} disabled={ro} onChange={(e) => setW({ start: e.target.value })} />
                <Input label={`Fim da janela ${i + 1}`} type="time" value={w.end} disabled={ro} onChange={(e) => setW({ end: e.target.value })} />
              </>
            )}
          />
        </>
      )
      break
    case 'customer_choice':
      form = <TextArea label="Pergunta" value={str(cfg, 'text')} disabled={ro} rows={3} helperText="Vazio usa: Sobre qual empresa é este atendimento?" onChange={(e) => set({ text: e.target.value || undefined })} />
      break
    case 'create_ticket':
      form = (
        <>
          <Input label="Assunto do chamado" value={str(cfg, 'subject')} disabled={ro} helperText={`Aceita {{variavel}}. ${varHint ?? ''}`} onChange={(e) => set({ subject: e.target.value })} />
          <Select label="Prioridade" value={str(cfg, 'priority') || 'medium'} disabled={ro} onChange={(v) => set({ priority: v })} hint="A prioridade é sempre uma escolha sua, por regra do fluxo: nem o contato nem a IA a definem.">
            <option value="low">Baixa</option>
            <option value="medium">Média</option>
            <option value="high">Alta</option>
            <option value="critical">Crítica</option>
          </Select>
        </>
      )
      break
    case 'assign_queue':
      form = queueSelect('Fila de destino', str(cfg, 'queue'), (v) => set({ queue: v }), false)
      break
    case 'human_handoff':
      form = (
        <>
          {queueSelect('Fila que recebe a conversa', typeof cfg.queue === 'string' ? cfg.queue : '', (v) => set({ queue: v || undefined }), true)}
          <TextArea label="Resumo para o atendente" value={str(cfg, 'summary')} disabled={ro} rows={4} helperText={`O que o atendente deve saber. Aceita {{variavel}}. ${varHint ?? ''}`} onChange={(e) => set({ summary: e.target.value })} />
        </>
      )
      break
    case 'subflow':
      form = (
        <Select label="Subfluxo" value={str(cfg, 'flow')} disabled={ro} onChange={(v) => set({ flow: v })} hint="Só aparecem subfluxos publicados. A versão é fixada na publicação deste fluxo.">
          <option value="">Selecione um subfluxo</option>
          {subflows.map((f) => <option key={f.id} value={f.slug}>{f.name}</option>)}
        </Select>
      )
      break
    case 'end':
      form = (
        <Select label="Resultado" value={str(cfg, 'outcome') || 'resolved'} disabled={ro} onChange={(v) => set({ outcome: v })}>
          <option value="resolved">Resolvido</option>
          <option value="abandoned">Abandonado</option>
          <option value="informational">Informativo</option>
        </Select>
      )
      break
    case 'ai_classify_intent':
      form = (
        <>
          <p className="rounded-control bg-status-info-soft p-2 text-xs text-text-secondary">A IA apenas sugere a intenção. Com pouca confiança, ou se ela estiver indisponível, o fluxo segue as saídas "baixa confiança" e "erro", que você precisa ligar.</p>
          <ListEditor<{ id: string; label: string; description?: string }>
            title="Intenções" items={arr(cfg, 'intents')} min={2} max={10} disabled={ro} addLabel="Adicionar intenção"
            blank={() => ({ id: `intencao_${arr(cfg, 'intents').length + 1}`, label: '' })}
            onChange={(intents) => set({ intents })}
            row={(it, setI, i) => (
              <>
                <Input label={`Nome da intenção ${i + 1}`} value={it.label} disabled={ro} onChange={(e) => setI({ label: e.target.value, id: it.id.startsWith('intencao_') && slug(e.target.value) ? slug(e.target.value) : it.id })} />
                <Input label={`Identificador da intenção ${i + 1}`} value={it.id} disabled={ro} onChange={(e) => setI({ id: e.target.value })} />
                <Input label={`Descrição da intenção ${i + 1}`} value={it.description ?? ''} disabled={ro} helperText="Ajuda a IA a distinguir" onChange={(e) => setI({ description: e.target.value || undefined })} />
              </>
            )}
          />
          <Input label="Confiança mínima (0 a 1)" type="number" step={0.05} min={0} max={1} value={str(cfg, 'min_confidence')} disabled={ro} onChange={(e) => set({ min_confidence: e.target.value === '' ? undefined : Number(e.target.value) })} />
        </>
      )
      break
    case 'ai_extract':
      form = (
        <ListEditor<{ variable: string; type: string; description?: string }>
          title="Campos a extrair" items={arr(cfg, 'fields')} min={1} max={10} disabled={ro} addLabel="Adicionar campo"
          blank={() => ({ variable: '', type: 'string' })}
          onChange={(fields) => set({ fields })}
          row={(f, setF, i) => (
            <>
              <Input label={`Variável do campo ${i + 1}`} value={f.variable} disabled={ro} onChange={(e) => setF({ variable: e.target.value })} />
              <Select label={`Tipo do campo ${i + 1}`} value={f.type} disabled={ro} onChange={(v) => setF({ type: v })}>
                <option value="string">Texto</option>
                <option value="number">Número</option>
                <option value="boolean">Sim/não</option>
                <option value="email">E-mail</option>
                <option value="phone">Telefone</option>
              </Select>
            </>
          )}
        />
      )
      break
    case 'ai_summarize':
      form = (
        <>
          <Input label="Salvar o resumo na variável" value={str(cfg, 'variable')} disabled={ro} onChange={(e) => set({ variable: e.target.value })} />
          <Input label="Máximo de mensagens" type="number" min={1} max={30} value={str(cfg, 'max_messages')} disabled={ro} onChange={(e) => set({ max_messages: e.target.value === '' ? undefined : Number(e.target.value) })} />
        </>
      )
      break
    default:
      form = <p className="text-sm text-text-secondary">Este nó não tem propriedades para editar.</p>
  }

  return (
    <section aria-label={`Propriedades de ${nodeLabel(node.type)}`} className="space-y-4">
      <header>
        <h2 className="text-base font-semibold text-text-primary">{nodeLabel(node.type)}</h2>
        <p className="text-xs text-text-tertiary">Identificador: {node.id}</p>
      </header>
      {form}
      {!ro && (
        <Button type="button" variant="danger" size="sm" onClick={onDelete}>
          Excluir nó
        </Button>
      )}
    </section>
  )
}
