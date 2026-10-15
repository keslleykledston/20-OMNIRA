import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import NodeInspector from '../components/flows/NodeInspector'
import { NODE_LABELS, defaultConfig, portsOf } from '../lib/flowModel'
import type { FlowNode } from '../lib/flows'

// ADR-0049 phase 6: the five service-session nodes in the visual editor.

const node = (type: string, config?: Record<string, unknown>): FlowNode => ({ id: 'n', type, position: { x: 0, y: 0 }, config })

function setup(n: FlowNode) {
  const onConfig = vi.fn()
  render(<NodeInspector node={n} queues={[]} sectors={[]} subflows={[]} variables={[]} onConfig={onConfig} onDelete={() => {}} />)
  return onConfig
}

const TYPES = ['start_service_session', 'classify_service_session', 'create_service_task', 'open_service_case', 'close_service_session']

describe('Nós do atendimento com protocolo (ADR-0049)', () => {
  it('each node has a label, a default config and the next/error ports (error optional)', () => {
    for (const t of TYPES) {
      expect(NODE_LABELS[t]).toBeTruthy()
      expect(defaultConfig(t)).toBeDefined()
      const ports = portsOf(node(t, defaultConfig(t)))
      expect(ports.map((p) => p.name)).toEqual(['next', 'error'])
      expect(ports.find((p) => p.name === 'error')?.required).toBe(false)
    }
  })

  it('closing the protocol is not closing the conversation: different labels, different ports', () => {
    expect(NODE_LABELS.close_service_session).not.toBe(NODE_LABELS.close_conversation)
    expect(portsOf(node('close_conversation'))).toEqual([])
    expect(portsOf(node('close_service_session')).length).toBeGreaterThan(0)
  })

  it('start: edits the variable that receives the protocol', () => {
    const onConfig = setup(node('start_service_session', { variable: 'protocolo' }))
    fireEvent.change(screen.getByLabelText('Guardar o protocolo na variável'), { target: { value: 'meu_protocolo' } })
    expect(onConfig).toHaveBeenLastCalledWith({ variable: 'meu_protocolo' })
  })

  it('classify: edits the category code', () => {
    const onConfig = setup(node('classify_service_session', { category: '' }))
    fireEvent.change(screen.getByLabelText('Código da categoria'), { target: { value: 'suporte' } })
    expect(onConfig).toHaveBeenLastCalledWith({ category: 'suporte' })
  })

  it('task: title, description and priority; nobody can be assigned from the flow', () => {
    const onConfig = setup(node('create_service_task', { title: '', priority: 'normal' }))
    fireEvent.change(screen.getByLabelText('Título da tarefa'), { target: { value: 'Ligar para {{contact.name}}' } })
    expect(onConfig).toHaveBeenLastCalledWith({ title: 'Ligar para {{contact.name}}', priority: 'normal' })
    fireEvent.change(screen.getByLabelText('Prioridade'), { target: { value: 'high' } })
    expect(onConfig).toHaveBeenLastCalledWith({ title: '', priority: 'high' })
    expect(screen.queryByLabelText(/respons[áa]vel|atribu/i)).toBeNull()
  })

  it('case: title, priority and the variable that receives the number', () => {
    const onConfig = setup(node('open_service_case', { title: 'Falha', priority: 'normal' }))
    fireEvent.change(screen.getByLabelText('Guardar o número na variável'), { target: { value: 'ocorrencia' } })
    expect(onConfig).toHaveBeenLastCalledWith({ title: 'Falha', priority: 'normal', variable: 'ocorrencia' })
  })

  it('close: result and note, and it says the conversation stays open', () => {
    const onConfig = setup(node('close_service_session', { result: 'information_provided' }))
    expect(screen.getByText(/A conversa continua aberta/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Resultado'), { target: { value: 'task_created' } })
    expect(onConfig).toHaveBeenLastCalledWith({ result: 'task_created' })
    // "abandoned" is the system's word for a session nobody finished: a flow does not pick it here
    const options = Array.from((screen.getByLabelText('Resultado') as HTMLSelectElement).options).map((o) => o.value)
    expect(options).not.toContain('abandoned')
  })
})
