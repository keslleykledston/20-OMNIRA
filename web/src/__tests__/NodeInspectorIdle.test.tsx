import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import NodeInspector from '../components/flows/NodeInspector'
import { portsOf, defaultConfig } from '../lib/flowModel'
import type { FlowNode } from '../lib/flows'

const node = (type: string, config?: Record<string, unknown>): FlowNode => ({ id: 'n', type, position: { x: 0, y: 0 }, config })

function setup(n: FlowNode) {
  const onConfig = vi.fn()
  render(<NodeInspector node={n} queues={[]} sectors={[]} subflows={[]} variables={[]} onConfig={onConfig} onDelete={() => {}} />)
  return onConfig
}

const menu = { text: 'Como ajudar?', variable: 'assunto', options: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }] }

describe('Menu sem resposta — lembretes e encerramento por inatividade (ADR-0045)', () => {
  it('o menu edita o tempo de espera e até 3 lembretes', () => {
    const onConfig = setup(node('choice', menu))
    fireEvent.change(screen.getByLabelText('Tempo de espera (segundos)'), { target: { value: '120' } })
    expect(onConfig).toHaveBeenLastCalledWith(expect.objectContaining({ timeout_seconds: 120, text: 'Como ajudar?' }))
    fireEvent.click(screen.getByRole('button', { name: 'Adicionar lembrete' }))
    expect(onConfig).toHaveBeenLastCalledWith(expect.objectContaining({ reminders: [''] }))
  })

  it('cada lembrete é um texto e o último removido limpa o campo', () => {
    const onConfig = setup(node('choice', { ...menu, reminders: ['Ainda aí?'] }))
    const field = screen.getByLabelText('Lembrete 1') as HTMLTextAreaElement
    expect(field.value).toBe('Ainda aí?')
    fireEvent.change(field, { target: { value: 'Escolha uma opção' } })
    expect(onConfig).toHaveBeenLastCalledWith(expect.objectContaining({ reminders: ['Escolha uma opção'] }))
    fireEvent.click(screen.getByRole('button', { name: 'Remover lembretes se o cliente não escolher 1' }))
    expect(onConfig).toHaveBeenLastCalledWith(expect.objectContaining({ reminders: undefined }))
  })

  it('o limite é de 3 lembretes', () => {
    setup(node('choice', { ...menu, reminders: ['1', '2', '3'] }))
    expect(screen.queryByRole('button', { name: 'Adicionar lembrete' })).toBeNull()
  })

  it('encerrar atendimento: mensagem, motivo e nenhuma saída', () => {
    const onConfig = setup(node('close_conversation', { text: 'Tchau', reason: 'no_response' }))
    expect((screen.getByLabelText('Mensagem de encerramento') as HTMLTextAreaElement).value).toBe('Tchau')
    fireEvent.change(screen.getByLabelText('Motivo registrado'), { target: { value: 'resolved' } })
    expect(onConfig).toHaveBeenLastCalledWith({ text: 'Tchau', reason: 'resolved' })
    expect(portsOf(node('close_conversation'))).toEqual([])
    expect(defaultConfig('close_conversation')).toMatchObject({ reason: 'no_response' })
  })
})
