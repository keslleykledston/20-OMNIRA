import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import NodeInspector from '../components/flows/NodeInspector'
import type { FlowNode } from '../lib/flows'

const queues = [{ id: 'q1', name: 'Cobrança' }]
const sectors = [
  { id: 's1', name: 'Financeiro', active: true },
  { id: 's2', name: 'Antigo', active: false },
]
const node = (type: string, config?: Record<string, unknown>): FlowNode => ({ id: 'n', type, position: { x: 0, y: 0 }, config })

function setup(n: FlowNode) {
  const onConfig = vi.fn()
  render(<NodeInspector node={n} queues={queues} sectors={sectors} subflows={[]} variables={[]} onConfig={onConfig} onDelete={() => {}} />)
  return onConfig
}

describe('NodeInspector — nós de roteamento', () => {
  it('consultar horário escolhe setor OU fila, nunca os dois, e marca setor inativo', () => {
    const onConfig = setup(node('check_schedule', { queue: 'q1' }))
    const select = screen.getByLabelText('Consultar o horário de') as HTMLSelectElement
    expect(select.value).toBe('queue:q1')
    expect(screen.getByRole('option', { name: 'Antigo (inativo)' })).toBeTruthy()
    fireEvent.change(select, { target: { value: 'sector:s1' } })
    expect(onConfig).toHaveBeenCalledWith({ queue: undefined, sector: 's1' })
  })

  it('um destino que não existe mais continua visível em vez de sumir', () => {
    setup(node('check_availability', { sector: 'gone' }))
    expect(screen.getByRole('option', { name: 'Item que não existe mais' })).toBeTruthy()
  })

  it('direcionar para setor aceita o setor que o cliente escolheu', () => {
    const onConfig = setup(node('assign_sector', {}))
    fireEvent.change(screen.getByLabelText('Setor'), { target: { value: '__variable' } })
    expect(onConfig).toHaveBeenCalledWith({ sector: undefined, from_variable: 'sector' })
  })

  it('aplicar transbordo não tem configuração a editar', () => {
    setup(node('apply_overflow'))
    expect(screen.getByText(/Não há o que configurar aqui/)).toBeTruthy()
  })
})
