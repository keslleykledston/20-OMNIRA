import type { FlowDefinition, FlowEdge, FlowNode, Issue, NodeTypeInfo } from './flows'

// Modelo puro do editor visual: nenhuma regra de negócio é decidida aqui (o backend valida e executa); isto só mantém o grafo
// coerente enquanto a pessoa edita, calcula as portas de saída que o nó tem com a configuração atual e a geometria do canvas.

export const NODE_WIDTH = 232
export const HEADER_HEIGHT = 38
export const PORT_ROW = 26
export const GRID = 20

export const EMPTY_DEFINITION: FlowDefinition = { schema_version: 1, nodes: [], edges: [], variables: [], settings: {}, metadata: {} }

export const NODE_LABELS: Record<string, string> = {
  trigger: 'Início',
  send_message: 'Enviar mensagem',
  ask: 'Fazer pergunta',
  choice: 'Menu de opções',
  condition: 'Condição',
  switch: 'Escolha por valor',
  set_variable: 'Definir variável',
  business_hours: 'Horário comercial',
  resolve_contact: 'Identificar contato',
  resolve_customer_context: 'Identificar empresa',
  customer_choice: 'Perguntar a empresa',
  find_open_tickets: 'Buscar chamados abertos',
  create_ticket: 'Abrir chamado',
  assign_queue: 'Direcionar para fila',
  human_handoff: 'Transferir para humano',
  subflow: 'Executar subfluxo',
  ai_classify_intent: 'IA: classificar intenção',
  ai_extract: 'IA: extrair dados',
  ai_summarize: 'IA: resumir conversa',
  end: 'Encerrar',
}

export const CATEGORY_LABELS: Record<string, string> = {
  flow: 'Fluxo',
  conversation: 'Conversa',
  logic: 'Lógica',
  data: 'Dados',
  action: 'Ações',
  ai: 'Inteligência artificial',
}

export const PORT_LABELS: Record<string, string> = {
  next: 'segue',
  timeout: 'sem resposta',
  error: 'erro',
  window_closed: 'janela de 24h fechada',
  true: 'sim',
  false: 'não',
  default: 'nenhum dos casos',
  other: 'outra resposta',
  open: 'aberto',
  closed: 'fechado',
  known: 'conhecido',
  unknown: 'desconhecido',
  none: 'nenhum',
  single: 'uma empresa',
  multiple: 'várias empresas',
  selected: 'escolheu',
  found: 'encontrou',
  low_confidence: 'baixa confiança',
}

export function nodeLabel(type: string): string {
  return NODE_LABELS[type] ?? type
}

export function portLabel(port: string): string {
  return PORT_LABELS[port] ?? port
}

interface ChoiceCfg { options?: { id: string; label?: string }[] }
interface SwitchCfg { cases?: { id: string }[] }
interface AIIntentCfg { intents?: { id: string; label?: string }[] }

// Portas de saída do nó com a configuração atual. Espelha o catálogo do backend (internal/flows/domain/nodes.go): o backend
// continua sendo quem valida; isto só decide o que desenhar. `required` marca as portas que o publicador exige ligadas.
export interface PortDef {
  name: string
  label: string
  required: boolean
}

const req = (name: string, label?: string): PortDef => ({ name, label: label ?? portLabel(name), required: true })
const opt = (name: string, label?: string): PortDef => ({ name, label: label ?? portLabel(name), required: false })

export function portsOf(node: FlowNode): PortDef[] {
  const cfg = (node.config ?? {}) as Record<string, unknown>
  switch (node.type) {
    case 'trigger':
    case 'set_variable':
    case 'subflow':
      return [req('next')]
    case 'send_message':
      return [req('next'), opt('window_closed'), opt('error')]
    case 'ask':
      return [req('next'), req('timeout'), opt('window_closed'), opt('error')]
    case 'choice': {
      const options = ((cfg as ChoiceCfg).options ?? []).filter((o) => o.id)
      return [...options.map((o) => req(o.id, o.label || o.id)), req('timeout'), opt('other'), opt('window_closed'), opt('error')]
    }
    case 'condition':
      return [req('true'), req('false')]
    case 'switch': {
      const cases = ((cfg as SwitchCfg).cases ?? []).filter((c) => c.id)
      return [...cases.map((c) => req(c.id, c.id)), req('default')]
    }
    case 'business_hours':
      return [req('open'), req('closed')]
    case 'resolve_contact':
      return [req('known'), req('unknown')]
    case 'resolve_customer_context':
      return [req('none'), req('single'), req('multiple')]
    case 'customer_choice':
      return [req('selected'), req('timeout'), opt('window_closed'), opt('error')]
    case 'find_open_tickets':
      return [req('none'), req('found')]
    case 'create_ticket':
    case 'assign_queue':
      return [req('next'), opt('error')]
    case 'ai_classify_intent': {
      const intents = ((cfg as AIIntentCfg).intents ?? []).filter((i) => i.id)
      return [...intents.map((i) => req(i.id, i.label || i.id)), req('low_confidence'), req('error')]
    }
    case 'ai_extract':
    case 'ai_summarize':
      return [req('next'), req('error')]
    case 'human_handoff':
    case 'end':
      return []
    default:
      return []
  }
}

// ---- geometria ---------------------------------------------------------------------------------------------------------

export function nodeHeight(node: FlowNode): number {
  return HEADER_HEIGHT + Math.max(1, portsOf(node).length) * PORT_ROW + 8
}

export interface Point {
  x: number
  y: number
}

// Ponto de onde a aresta sai: a borda direita do cartão, na linha da porta.
export function portAnchor(node: FlowNode, port: string): Point {
  const ports = portsOf(node)
  const i = Math.max(0, ports.findIndex((p) => p.name === port))
  return { x: node.position.x + NODE_WIDTH, y: node.position.y + HEADER_HEIGHT + i * PORT_ROW + PORT_ROW / 2 }
}

// Ponto onde a aresta chega: a borda esquerda do cartão, no meio do cabeçalho.
export function inputAnchor(node: FlowNode): Point {
  return { x: node.position.x, y: node.position.y + HEADER_HEIGHT / 2 }
}

export function edgePath(from: Point, to: Point): string {
  const dx = Math.max(60, Math.abs(to.x - from.x) / 2)
  return `M ${from.x} ${from.y} C ${from.x + dx} ${from.y}, ${to.x - dx} ${to.y}, ${to.x} ${to.y}`
}

export function snap(v: number, grid = GRID): number {
  return Math.max(0, Math.round(v / grid) * grid)
}

export function canvasSize(def: FlowDefinition): { width: number; height: number } {
  let w = 1200
  let h = 700
  for (const n of def.nodes) {
    w = Math.max(w, n.position.x + NODE_WIDTH + 200)
    h = Math.max(h, n.position.y + nodeHeight(n) + 200)
  }
  return { width: w, height: h }
}

// ---- edição imutável do grafo ------------------------------------------------------------------------------------------

export function nextNodeId(def: FlowDefinition, type: string): string {
  const stem = type.replace(/[^a-z0-9]+/gi, '_').toLowerCase()
  const used = new Set(def.nodes.map((n) => n.id))
  if (!used.has(stem)) return stem
  for (let i = 2; ; i++) {
    const id = `${stem}_${i}`
    if (!used.has(id)) return id
  }
}

// Configuração inicial que já valida para o tipo (a pessoa só completa os textos). Os mesmos padrões do backend.
export function defaultConfig(type: string): Record<string, unknown> | undefined {
  switch (type) {
    case 'send_message':
      return { text: '' }
    case 'ask':
      return { text: '', variable: '' }
    case 'choice':
      return { text: '', variable: '', options: [{ id: 'opcao_1', label: 'Opção 1' }, { id: 'opcao_2', label: 'Opção 2' }] }
    case 'condition':
      return { variable: '', op: 'eq', value: '' }
    case 'switch':
      return { variable: '', cases: [{ id: 'caso_1', value: '' }] }
    case 'set_variable':
      return { assignments: [{ variable: '', value: '' }] }
    case 'business_hours':
      return { timezone: 'America/Sao_Paulo', windows: [{ days: ['mon', 'tue', 'wed', 'thu', 'fri'], start: '08:00', end: '18:00' }] }
    case 'create_ticket':
      return { subject: '', priority: 'medium' }
    case 'subflow':
      return { flow: '' }
    case 'end':
      return { outcome: 'resolved' }
    case 'human_handoff':
      return { summary: '' }
    case 'ai_classify_intent':
      return { intents: [{ id: 'intencao_1', label: 'Intenção 1' }, { id: 'intencao_2', label: 'Intenção 2' }], min_confidence: 0.7 }
    case 'ai_extract':
      return { fields: [{ variable: '', type: 'string' }] }
    case 'ai_summarize':
      return { variable: 'resumo', max_messages: 15 }
    default:
      return undefined
  }
}

// Onde nasce um nó novo: à direita do nó âncora (o selecionado ou o último), que é o sentido em que um fluxo é lido; se esse
// lugar estiver ocupado, desce na mesma coluna e depois passa à coluna seguinte. Nunca nasce em cima de outro nó; "Organizar"
// refaz o layout por camadas quando o desenho já tiver crescido.
export function freePosition(def: FlowDefinition, height: number, anchor?: FlowNode): Point {
  const colStep = NODE_WIDTH + 80
  const margin = 24
  const overlaps = (x: number, y: number) =>
    def.nodes.some((n) => x < n.position.x + NODE_WIDTH + margin && x + NODE_WIDTH + margin > n.position.x && y < n.position.y + nodeHeight(n) + margin && y + height + margin > n.position.y)
  const maxY = def.nodes.reduce((m, n) => Math.max(m, n.position.y + nodeHeight(n)), 0) + 400
  const startCol = anchor ? Math.round((anchor.position.x - 40) / colStep) + 1 : 0
  const startY = anchor ? Math.max(40, snap(anchor.position.y)) : 40
  for (let col = startCol; col < startCol + 12; col++) {
    for (let y = col === startCol ? startY : 40; y <= maxY; y += GRID) {
      const x = snap(40 + col * colStep)
      if (!overlaps(x, y)) return { x, y }
    }
  }
  return { x: 40, y: maxY }
}

// `after` é o id do nó a partir do qual o novo deve crescer; sem ele, usa o último nó do desenho.
export function addNode(def: FlowDefinition, type: string, at?: Point, after?: string): { def: FlowDefinition; id: string } {
  const id = nextNodeId(def, type)
  const node: FlowNode = { id, type, position: { x: 0, y: 0 } }
  const cfg = defaultConfig(type)
  if (cfg) node.config = cfg
  const anchor = def.nodes.find((n) => n.id === after) ?? def.nodes[def.nodes.length - 1]
  node.position = at ? { x: snap(at.x), y: snap(at.y) } : freePosition(def, nodeHeight(node), anchor)
  return { def: { ...def, nodes: [...def.nodes, node] }, id }
}

export function updateNode(def: FlowDefinition, id: string, patch: Partial<FlowNode>): FlowDefinition {
  return { ...def, nodes: def.nodes.map((n) => (n.id === id ? { ...n, ...patch } : n)) }
}

export function moveNode(def: FlowDefinition, id: string, to: Point): FlowDefinition {
  return updateNode(def, id, { position: { x: snap(to.x), y: snap(to.y) } })
}

// Quando a configuração muda as portas (opções do menu, casos, intenções), arestas cujas portas deixaram de existir saem junto:
// o canvas nunca guarda uma aresta pendurada em uma porta que o nó não tem.
export function updateConfig(def: FlowDefinition, id: string, config: Record<string, unknown>): FlowDefinition {
  const next = updateNode(def, id, { config })
  const node = next.nodes.find((n) => n.id === id)
  if (!node) return next
  const valid = new Set(portsOf(node).map((p) => p.name))
  return { ...next, edges: next.edges.filter((e) => e.source !== id || valid.has(e.sourcePort)) }
}

export function removeNode(def: FlowDefinition, id: string): FlowDefinition {
  return { ...def, nodes: def.nodes.filter((n) => n.id !== id), edges: def.edges.filter((e) => e.source !== id && e.target !== id) }
}

export type ConnectResult = { ok: true; def: FlowDefinition } | { ok: false; reason: string }

// Uma porta tem um único destino (regra do validador). Ligar de novo a mesma porta substitui o destino anterior.
export function connect(def: FlowDefinition, source: string, sourcePort: string, target: string): ConnectResult {
  const src = def.nodes.find((n) => n.id === source)
  const dst = def.nodes.find((n) => n.id === target)
  if (!src || !dst) return { ok: false, reason: 'Nó inexistente.' }
  if (dst.type === 'trigger') return { ok: false, reason: 'O início não pode receber ligações.' }
  if (src.id === dst.id) return { ok: false, reason: 'Um nó não pode ser ligado a ele mesmo.' }
  if (!portsOf(src).some((p) => p.name === sourcePort)) return { ok: false, reason: 'Este nó não tem essa saída.' }
  if (reaches(def, target, source)) return { ok: false, reason: 'Essa ligação criaria um ciclo: fluxos não podem voltar para um passo anterior.' }
  const kept = def.edges.filter((e) => !(e.source === source && e.sourcePort === sourcePort))
  const id = nextEdgeId(kept)
  return { ok: true, def: { ...def, edges: [...kept, { id, source, sourcePort, target }] } }
}

export function disconnect(def: FlowDefinition, edgeId: string): FlowDefinition {
  return { ...def, edges: def.edges.filter((e) => e.id !== edgeId) }
}

function nextEdgeId(edges: FlowEdge[]): string {
  const used = new Set(edges.map((e) => e.id))
  for (let i = edges.length + 1; ; i++) {
    const id = `e${i}`
    if (!used.has(id)) return id
  }
}

// `from` alcança `to` seguindo as arestas? Usado para impedir ciclos já na hora da ligação.
export function reaches(def: FlowDefinition, from: string, to: string): boolean {
  const out = new Map<string, string[]>()
  for (const e of def.edges) out.set(e.source, [...(out.get(e.source) ?? []), e.target])
  const seen = new Set<string>()
  const stack = [from]
  while (stack.length) {
    const cur = stack.pop() as string
    if (cur === to) return true
    if (seen.has(cur)) continue
    seen.add(cur)
    stack.push(...(out.get(cur) ?? []))
  }
  return false
}

// Portas obrigatórias sem destino (para sinalizar no cartão antes mesmo de validar no servidor).
export function openRequiredPorts(def: FlowDefinition, node: FlowNode): string[] {
  const used = new Set(def.edges.filter((e) => e.source === node.id).map((e) => e.sourcePort))
  return portsOf(node).filter((p) => p.required && !used.has(p.name)).map((p) => p.name)
}

// ---- layout automático -------------------------------------------------------------------------------------------------

// Camadas da esquerda para a direita (profundidade mais longa a partir do início), sem sobreposição vertical.
export function autoLayout(def: FlowDefinition): FlowDefinition {
  const start = def.nodes.find((n) => n.type === 'trigger')
  if (!start) return def
  const out = new Map<string, string[]>()
  for (const e of def.edges) out.set(e.source, [...(out.get(e.source) ?? []), e.target])
  const depth = new Map<string, number>([[start.id, 0]])
  const queue = [start.id]
  for (let guard = 0; queue.length && guard < 10000; guard++) {
    const cur = queue.shift() as string
    for (const nx of out.get(cur) ?? []) {
      const d = (depth.get(cur) ?? 0) + 1
      if (d > (depth.get(nx) ?? -1) && d <= def.nodes.length) {
        depth.set(nx, d)
        queue.push(nx)
      }
    }
  }
  const cols = new Map<number, number>()
  const orphans = def.nodes.filter((n) => !depth.has(n.id))
  const maxDepth = Math.max(0, ...depth.values())
  const place = (n: FlowNode, d: number): FlowNode => {
    const y = cols.get(d) ?? 40
    cols.set(d, y + nodeHeight(n) + 40)
    return { ...n, position: { x: 40 + d * (NODE_WIDTH + 80), y } }
  }
  const nodes = def.nodes.map((n) => (depth.has(n.id) ? place(n, depth.get(n.id) as number) : n))
  const orphanPlaced = orphans.map((n) => place(n, maxDepth + 1))
  const byId = new Map([...nodes, ...orphanPlaced].map((n) => [n.id, n]))
  return { ...def, nodes: def.nodes.map((n) => byId.get(n.id) as FlowNode) }
}

// ---- problemas por nó/aresta -------------------------------------------------------------------------------------------

export function issuesByNode(issues: Issue[]): Map<string, Issue[]> {
  const m = new Map<string, Issue[]>()
  for (const i of issues) {
    if (!i.node_id) continue
    m.set(i.node_id, [...(m.get(i.node_id) ?? []), i])
  }
  return m
}

export function hasBlocking(issues: Issue[]): boolean {
  return issues.some((i) => i.severity === 'error')
}

export function groupNodeTypes(types: NodeTypeInfo[]): { category: string; label: string; items: NodeTypeInfo[] }[] {
  const order = ['flow', 'conversation', 'logic', 'data', 'action', 'ai']
  const groups = new Map<string, NodeTypeInfo[]>()
  for (const t of types) groups.set(t.category, [...(groups.get(t.category) ?? []), t])
  return [...groups.entries()]
    .sort((a, b) => order.indexOf(a[0]) - order.indexOf(b[0]))
    .map(([category, items]) => ({ category, label: CATEGORY_LABELS[category] ?? category, items }))
}

export function stableJSON(def: FlowDefinition): string {
  return JSON.stringify(def)
}
