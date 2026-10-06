import { useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import type { FlowDefinition, FlowNode, Issue } from '../../lib/flows'
import {
  NODE_WIDTH,
  HEADER_HEIGHT,
  PORT_ROW,
  canvasSize,
  edgePath,
  inputAnchor,
  issuesByNode,
  nodeHeight,
  nodeLabel,
  openRequiredPorts,
  portAnchor,
  portLabel,
  portsOf,
} from '../../lib/flowModel'

export interface PendingConnection {
  source: string
  port: string
}

interface Props {
  def: FlowDefinition
  selectedId: string | null
  issues: Issue[]
  pending: PendingConnection | null
  readOnly?: boolean
  highlight?: string[] // nó(s) destacados (ex.: caminho de uma simulação)
  onSelect: (id: string | null) => void
  onMove: (id: string, pos: { x: number; y: number }) => void
  onStartConnect: (c: PendingConnection | null) => void
  onFinishConnect: (target: string) => void
  onDisconnect: (edgeId: string) => void
}

// Canvas sem dependências: cartões posicionados em um plano rolável e arestas em um SVG por baixo. Ligar é "clique na saída,
// clique no nó de destino" (acessível por teclado e testável); mover é arrastar o cabeçalho. A validade da ligação é decidida
// por flowModel.connect; o canvas só mostra o motivo quando ela é recusada.
export default function FlowCanvas({ def, selectedId, issues, pending, readOnly, highlight, onSelect, onMove, onStartConnect, onFinishConnect, onDisconnect }: Props) {
  const size = canvasSize(def)
  const byNode = issuesByNode(issues)
  const nodeById = new Map(def.nodes.map((n) => [n.id, n]))
  const drag = useRef<{ id: string; dx: number; dy: number } | null>(null)
  const [dragPos, setDragPos] = useState<{ id: string; x: number; y: number } | null>(null)
  const planeRef = useRef<HTMLDivElement>(null)
  const scrollerRef = useRef<HTMLDivElement>(null)

  // Um nó recém-adicionado ou selecionado por outro painel (um problema, o simulador) pode estar fora da área visível: o canvas rola até ele.
  useEffect(() => {
    if (!selectedId) return
    const el = scrollerRef.current?.querySelector<HTMLElement>(`[data-node-id="${CSS.escape(selectedId)}"]`)
    el?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
  }, [selectedId])

  const positioned = useCallback(
    (n: FlowNode): FlowNode => (dragPos && dragPos.id === n.id ? { ...n, position: { x: dragPos.x, y: dragPos.y } } : n),
    [dragPos],
  )

  useEffect(() => {
    if (!dragPos) return undefined
    const move = (e: MouseEvent) => {
      const d = drag.current
      const rect = planeRef.current?.getBoundingClientRect()
      if (!d || !rect) return
      setDragPos({ id: d.id, x: Math.max(0, e.clientX - rect.left - d.dx), y: Math.max(0, e.clientY - rect.top - d.dy) })
    }
    const up = () => {
      const d = drag.current
      drag.current = null
      setDragPos((cur) => {
        if (cur && d) onMove(d.id, { x: cur.x, y: cur.y })
        return null
      })
    }
    window.addEventListener('mousemove', move)
    window.addEventListener('mouseup', up)
    return () => {
      window.removeEventListener('mousemove', move)
      window.removeEventListener('mouseup', up)
    }
    // a drag session is bound to the node being dragged
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dragPos?.id])

  const beginDrag = (e: React.MouseEvent, n: FlowNode) => {
    if (readOnly || e.button !== 0) return
    e.preventDefault() // sem isso o navegador seleciona o texto do cabeçalho enquanto se arrasta
    const rect = planeRef.current?.getBoundingClientRect()
    if (!rect) return
    drag.current = { id: n.id, dx: e.clientX - rect.left - n.position.x, dy: e.clientY - rect.top - n.position.y }
    setDragPos({ id: n.id, x: n.position.x, y: n.position.y })
    onSelect(n.id)
  }

  return (
    <div ref={scrollerRef} className="relative h-full w-full overflow-auto bg-surface-muted" data-testid="flow-canvas" onClick={() => { onSelect(null); onStartConnect(null) }}>
      <div ref={planeRef} className="relative" style={{ width: size.width, height: size.height }}>
        <svg className="absolute inset-0" width={size.width} height={size.height} aria-hidden={false}>
          {def.edges.map((e) => {
            const src = nodeById.get(e.source)
            const dst = nodeById.get(e.target)
            if (!src || !dst) return null
            const from = portAnchor(positioned(src), e.sourcePort)
            const to = inputAnchor(positioned(dst))
            const mid = { x: (from.x + to.x) / 2, y: (from.y + to.y) / 2 }
            const lit = !!highlight && highlight.includes(e.source) && highlight.includes(e.target)
            return (
              <g key={e.id} data-edge-id={e.id}>
                <path d={edgePath(from, to)} fill="none" strokeWidth={lit ? 3 : 2} className={clsx(lit ? 'stroke-accent-primary' : 'stroke-border-strong')} />
                {!readOnly && (
                  <g
                    role="button"
                    tabIndex={0}
                    aria-label={`Remover ligação de ${nodeLabel(src.type)} (${portLabel(e.sourcePort)}) para ${nodeLabel(dst.type)}`}
                    className="cursor-pointer"
                    onClick={(ev) => { ev.stopPropagation(); onDisconnect(e.id) }}
                    onKeyDown={(ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); onDisconnect(e.id) } }}
                  >
                    <circle cx={mid.x} cy={mid.y} r={9} className="fill-surface stroke-border-strong" />
                    <path d={`M ${mid.x - 3.5} ${mid.y - 3.5} l 7 7 M ${mid.x + 3.5} ${mid.y - 3.5} l -7 7`} className="stroke-text-secondary" strokeWidth={1.5} />
                  </g>
                )}
              </g>
            )
          })}
        </svg>

        {def.nodes.map((raw) => {
          const n = positioned(raw)
          const ports = portsOf(n)
          const nodeIssues = byNode.get(n.id) ?? []
          const blocking = nodeIssues.some((i) => i.severity === 'error')
          const warn = nodeIssues.length > 0 && !blocking
          const open = openRequiredPorts(def, n)
          const selected = selectedId === n.id
          const lit = !!highlight && highlight.includes(n.id)
          const canReceive = !!pending && n.type !== 'trigger' && pending.source !== n.id
          return (
            <div
              key={n.id}
              role="group"
              aria-label={`Nó ${nodeLabel(n.type)}: ${n.id}`}
              data-node-id={n.id}
              data-selected={selected || undefined}
              className={clsx(
                'absolute select-none rounded-card border bg-surface shadow-sm',
                blocking ? 'border-status-danger' : warn ? 'border-status-warning' : 'border-border-subtle',
                selected && 'ring-2 ring-accent-primary',
                lit && 'ring-2 ring-status-success',
              )}
              style={{ left: n.position.x, top: n.position.y, width: NODE_WIDTH, height: nodeHeight(n) }}
              onClick={(e) => { e.stopPropagation(); onSelect(n.id) }}
            >
              <div
                className={clsx('flex items-center justify-between gap-2 rounded-t-card bg-surface-muted px-3', readOnly ? 'cursor-default' : 'cursor-grab')}
                style={{ height: HEADER_HEIGHT }}
                onMouseDown={(e) => beginDrag(e, raw)}
              >
                <span className="truncate text-sm font-semibold text-text-primary" title={n.id}>{nodeLabel(n.type)}</span>
                {n.type !== 'trigger' && (
                  <button
                    type="button"
                    aria-label={`Entrada de ${nodeLabel(n.type)} (${n.id})`}
                    disabled={!canReceive}
                    onMouseDown={(e) => e.stopPropagation()}
                    onClick={(e) => { e.stopPropagation(); onFinishConnect(n.id) }}
                    className={clsx(
                      'h-4 w-4 shrink-0 rounded-full border-2',
                      canReceive ? 'border-accent-primary bg-accent-primary-soft animate-pulse' : 'border-border-strong bg-surface',
                    )}
                  />
                )}
              </div>
              <ul className="m-0 list-none p-0">
                {ports.length === 0 && (
                  <li className="flex items-center px-3 text-xs text-text-tertiary" style={{ height: PORT_ROW }}>
                    {n.type === 'human_handoff' ? 'entrega a conversa a uma pessoa' : 'fim do fluxo'}
                  </li>
                )}
                {ports.map((p) => {
                  const isPending = pending?.source === n.id && pending.port === p.name
                  const connected = def.edges.some((e) => e.source === n.id && e.sourcePort === p.name)
                  return (
                    <li key={p.name} className="flex items-center justify-between gap-2 pl-3" style={{ height: PORT_ROW }}>
                      <span className={clsx('truncate text-xs', open.includes(p.name) ? 'text-status-danger' : 'text-text-secondary')} title={p.label}>
                        {p.label}
                        {!p.required && <span className="text-text-tertiary"> (opcional)</span>}
                      </span>
                      <button
                        type="button"
                        disabled={readOnly}
                        aria-label={`Saída ${p.label} de ${nodeLabel(n.type)} (${n.id})`}
                        aria-pressed={isPending}
                        onMouseDown={(e) => e.stopPropagation()}
                        onClick={(e) => { e.stopPropagation(); onStartConnect(isPending ? null : { source: n.id, port: p.name }) }}
                        className={clsx(
                          '-mr-2 h-4 w-4 shrink-0 rounded-full border-2',
                          isPending ? 'border-accent-primary bg-accent-primary' : connected ? 'border-accent-primary bg-accent-primary-soft' : 'border-border-strong bg-surface',
                        )}
                      />
                    </li>
                  )
                })}
              </ul>
            </div>
          )
        })}
      </div>
      {def.nodes.length === 0 && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center text-sm text-text-tertiary">
          Adicione um nó <strong className="mx-1 text-text-secondary">Início</strong> pela paleta para começar.
        </div>
      )}
    </div>
  )
}
