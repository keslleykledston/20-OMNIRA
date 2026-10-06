import { describe, expect, it } from 'vitest'
import {
  EMPTY_DEFINITION,
  NODE_WIDTH,
  addNode,
  autoLayout,
  connect,
  disconnect,
  edgePath,
  groupNodeTypes,
  hasBlocking,
  inputAnchor,
  issuesByNode,
  moveNode,
  nodeHeight,
  nextNodeId,
  openRequiredPorts,
  portAnchor,
  portsOf,
  reaches,
  removeNode,
  snap,
  updateConfig,
} from '../lib/flowModel'
import type { FlowDefinition, FlowNode, NodeTypeInfo } from '../lib/flows'

function build(): FlowDefinition {
  let def: FlowDefinition = EMPTY_DEFINITION
  for (const t of ['trigger', 'send_message', 'choice', 'end']) def = addNode(def, t).def
  return def
}

const ids = (d: FlowDefinition) => d.nodes.map((n) => n.id)

// connect() devolve ok/def ou ok/reason: nos testes que esperam sucesso, extrai o grafo ou falha com o motivo.
function linked(def: FlowDefinition, source: string, port: string, target: string): FlowDefinition {
  const r = connect(def, source, port, target)
  if (!r.ok) throw new Error(`connect(${source}.${port} -> ${target}) refused: ${r.reason}`)
  return r.def
}

describe('portsOf', () => {
  it('derives choice ports from its options, then timeout (required) and other (optional)', () => {
    const n: FlowNode = { id: 'm', type: 'choice', position: { x: 0, y: 0 }, config: { options: [{ id: 'tech', label: 'Suporte' }, { id: 'fin', label: 'Financeiro' }] } }
    expect(portsOf(n).map((p) => [p.name, p.required])).toEqual([['tech', true], ['fin', true], ['timeout', true], ['other', false]])
    expect(portsOf(n)[0].label).toBe('Suporte')
  })

  it('derives switch and AI intent ports, with the mandatory fallbacks', () => {
    const sw: FlowNode = { id: 's', type: 'switch', position: { x: 0, y: 0 }, config: { cases: [{ id: 'a' }, { id: 'b' }] } }
    expect(portsOf(sw).map((p) => p.name)).toEqual(['a', 'b', 'default'])
    const ai: FlowNode = { id: 'c', type: 'ai_classify_intent', position: { x: 0, y: 0 }, config: { intents: [{ id: 'x', label: 'X' }, { id: 'y', label: 'Y' }] } }
    expect(portsOf(ai).filter((p) => p.required).map((p) => p.name)).toEqual(['x', 'y', 'low_confidence', 'error'])
  })

  it('terminal nodes have no outputs and each fixed node matches the backend catalog', () => {
    const n = (type: string): FlowNode => ({ id: type, type, position: { x: 0, y: 0 } })
    expect(portsOf(n('end'))).toEqual([])
    expect(portsOf(n('human_handoff'))).toEqual([])
    expect(portsOf(n('condition')).map((p) => p.name)).toEqual(['true', 'false'])
    expect(portsOf(n('resolve_customer_context')).map((p) => p.name)).toEqual(['none', 'single', 'multiple'])
    expect(portsOf(n('send_message')).map((p) => p.name)).toEqual(['next', 'window_closed', 'error'])
    expect(portsOf(n('ask')).filter((p) => p.required).map((p) => p.name)).toEqual(['next', 'timeout'])
    expect(portsOf(n('unknown_future_node'))).toEqual([])
  })
})

describe('adding, moving and removing nodes', () => {
  it('gives unique ids and a valid starting config', () => {
    let def = addNode(EMPTY_DEFINITION, 'send_message').def
    def = addNode(def, 'send_message').def
    expect(ids(def)).toEqual(['send_message', 'send_message_2'])
    expect(nextNodeId(def, 'send_message')).toBe('send_message_3')
    expect(def.nodes[0].config).toEqual({ text: '' })
    const choice = addNode(EMPTY_DEFINITION, 'choice').def.nodes[0]
    expect((choice.config as { options: unknown[] }).options).toHaveLength(2)
  })

  it('never places a new node on top of another, however many are added', () => {
    let def: FlowDefinition = EMPTY_DEFINITION
    for (const t of ['trigger', 'choice', 'send_message', 'ask', 'choice', 'condition', 'switch', 'end', 'end', 'human_handoff', 'ai_classify_intent', 'create_ticket', 'send_message', 'choice']) def = addNode(def, t).def
    for (let i = 0; i < def.nodes.length; i++) {
      for (let j = i + 1; j < def.nodes.length; j++) {
        const a = def.nodes[i]
        const b = def.nodes[j]
        const overlap = a.position.x < b.position.x + NODE_WIDTH && a.position.x + NODE_WIDTH > b.position.x && a.position.y < b.position.y + nodeHeight(b) && a.position.y + nodeHeight(a) > b.position.y
        expect(overlap, `${a.id} overlaps ${b.id}`).toBe(false)
      }
    }
    expect(def.nodes[0].position).toEqual({ x: 40, y: 40 })
    expect(def.nodes.every((n) => n.position.x % 20 === 0 && n.position.y % 20 === 0)).toBe(true)
  })

  it('grows the flow to the right of the selected node, and of the last one by default', () => {
    let def = addNode(EMPTY_DEFINITION, 'trigger').def
    def = addNode(def, 'send_message').def
    def = addNode(def, 'choice').def
    const xs = def.nodes.map((n) => n.position.x)
    expect(xs[1]).toBeGreaterThan(xs[0])
    expect(xs[2]).toBeGreaterThan(xs[1])
    // branching from the first node: the new node goes to ITS right (a free row), not after the last one
    const branch = addNode(def, 'end', undefined, 'trigger').def.nodes[3]
    expect(branch.position.x).toBe(def.nodes[1].position.x)
    expect(branch.position.y).not.toBe(def.nodes[1].position.y)
  })

  it('does not mutate its input', () => {
    const before = JSON.stringify(EMPTY_DEFINITION)
    addNode(EMPTY_DEFINITION, 'trigger')
    expect(JSON.stringify(EMPTY_DEFINITION)).toBe(before)
  })

  it('snaps positions to the grid and never goes negative', () => {
    expect(snap(33)).toBe(40)
    expect(snap(-50)).toBe(0)
    const def = moveNode(addNode(EMPTY_DEFINITION, 'end', { x: 11, y: 29 }).def, 'end', { x: 505, y: -3 })
    expect(def.nodes[0].position).toEqual({ x: 500, y: 0 })
  })

  it('removing a node removes the edges that touch it', () => {
    let def = build()
    def = linked(def, 'trigger', 'next', 'send_message')
    def = linked(def, 'send_message', 'next', 'end')
    const out = removeNode(def, 'send_message')
    expect(ids(out)).toEqual(['trigger', 'choice', 'end'])
    expect(out.edges).toHaveLength(0)
  })
})

describe('connect', () => {
  it('links an existing port to a node and replaces the previous destination of that port', () => {
    const def = build()
    const first = connect(def, 'send_message', 'next', 'choice')
    expect(first.ok).toBe(true)
    if (!first.ok) return
    const second = linked(first.def, 'send_message', 'next', 'end')
    expect(second.edges).toHaveLength(1)
    expect(second.edges[0]).toMatchObject({ source: 'send_message', sourcePort: 'next', target: 'end' })
  })

  it('refuses invalid links with a reason the operator can read', () => {
    const def = build()
    expect(connect(def, 'send_message', 'next', 'trigger')).toEqual({ ok: false, reason: 'O início não pode receber ligações.' })
    expect(connect(def, 'send_message', 'next', 'send_message')).toMatchObject({ ok: false })
    expect(connect(def, 'send_message', 'nope', 'end')).toEqual({ ok: false, reason: 'Este nó não tem essa saída.' })
    expect(connect(def, 'ghost', 'next', 'end')).toMatchObject({ ok: false })
    expect(connect(def, 'end', 'next', 'choice')).toMatchObject({ ok: false }) // a terminal has no outputs
  })

  it('refuses a link that would close a loop (flows must terminate)', () => {
    let def = build()
    def = linked(def, 'send_message', 'next', 'choice')
    const loop = connect(def, 'choice', 'timeout', 'send_message')
    expect(loop.ok).toBe(false)
    if (!loop.ok) expect(loop.reason).toMatch(/ciclo/)
    expect(reaches(def, 'send_message', 'choice')).toBe(true)
    expect(reaches(def, 'choice', 'send_message')).toBe(false)
  })

  it('disconnect removes only that edge', () => {
    let def = build()
    def = linked(def, 'send_message', 'next', 'choice')
    def = linked(def, 'choice', 'timeout', 'end')
    expect(disconnect(def, def.edges[0].id).edges).toHaveLength(1)
  })
})

describe('updateConfig keeps the graph coherent', () => {
  it('drops edges whose port disappeared when an option is removed or renamed', () => {
    let def = build()
    def = linked(def, 'choice', 'opcao_1', 'end')
    def = linked(def, 'choice', 'opcao_2', 'send_message')
    expect(def.edges).toHaveLength(2)
    const kept = updateConfig(def, 'choice', { text: 'x', variable: 'v', options: [{ id: 'opcao_1', label: 'Um' }, { id: 'novo', label: 'Novo' }] })
    expect(kept.edges.map((e) => e.sourcePort)).toEqual(['opcao_1'])
    const labelOnly = updateConfig(def, 'choice', { text: 'x', variable: 'v', options: [{ id: 'opcao_1', label: 'Renomeado' }, { id: 'opcao_2', label: 'Dois' }] })
    expect(labelOnly.edges).toHaveLength(2)
  })

  it('reports required ports that still have no destination', () => {
    let def = build()
    const choice = def.nodes.find((n) => n.id === 'choice') as FlowNode
    expect(openRequiredPorts(def, choice)).toEqual(['opcao_1', 'opcao_2', 'timeout'])
    def = linked(def, 'choice', 'timeout', 'end')
    expect(openRequiredPorts(def, choice)).toEqual(['opcao_1', 'opcao_2'])
  })
})

describe('geometry', () => {
  it('anchors edges at the port row and at the target header', () => {
    const n: FlowNode = { id: 'm', type: 'choice', position: { x: 100, y: 200 }, config: { options: [{ id: 'a' }, { id: 'b' }] } }
    expect(portAnchor(n, 'a')).toEqual({ x: 100 + NODE_WIDTH, y: 200 + 38 + 13 })
    expect(portAnchor(n, 'b').y - portAnchor(n, 'a').y).toBe(26)
    expect(inputAnchor(n)).toEqual({ x: 100, y: 219 })
    expect(edgePath({ x: 0, y: 0 }, { x: 300, y: 100 })).toBe('M 0 0 C 150 0, 150 100, 300 100')
  })
})

describe('autoLayout', () => {
  it('places nodes in left-to-right layers without overlaps and keeps orphans out of the way', () => {
    let def = build()
    def = linked(def, 'trigger', 'next', 'send_message')
    def = linked(def, 'send_message', 'next', 'choice')
    def = linked(def, 'choice', 'timeout', 'end')
    const laid = autoLayout(def)
    const x = (id: string) => (laid.nodes.find((n) => n.id === id) as FlowNode).position.x
    expect(x('trigger')).toBeLessThan(x('send_message'))
    expect(x('send_message')).toBeLessThan(x('choice'))
    expect(x('choice')).toBeLessThan(x('end'))
    const orphaned = autoLayout(addNode(def, 'end').def)
    const orphan = orphaned.nodes.find((n) => n.id === 'end_2') as FlowNode
    expect(orphan.position.x).toBeGreaterThan(x('end'))
    expect(autoLayout(EMPTY_DEFINITION)).toBe(EMPTY_DEFINITION)
  })
})

describe('issues', () => {
  it('groups issues by node and detects blocking ones', () => {
    const issues = [
      { severity: 'warning' as const, code: 'unconnected_optional_port', node_id: 'a', message: 'w' },
      { severity: 'error' as const, code: 'cycle', node_id: 'a', message: 'e' },
      { severity: 'error' as const, code: 'missing_trigger', message: 'no node' },
    ]
    expect(issuesByNode(issues).get('a')).toHaveLength(2)
    expect(issuesByNode(issues).has('undefined')).toBe(false)
    expect(hasBlocking(issues)).toBe(true)
    expect(hasBlocking([issues[0]])).toBe(false)
  })

  it('orders the palette by category', () => {
    const t = (type: string, category: string): NodeTypeInfo => ({ type, label: type, category, side_effect: 'none', waits: false, terminal: false, ports: [] })
    const groups = groupNodeTypes([t('ai_extract', 'ai'), t('end', 'flow'), t('ask', 'conversation'), t('create_ticket', 'action')])
    expect(groups.map((g) => g.category)).toEqual(['flow', 'conversation', 'action', 'ai'])
    expect(groups[3].label).toBe('Inteligência artificial')
  })
})
