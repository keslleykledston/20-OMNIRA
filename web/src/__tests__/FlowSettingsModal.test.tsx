import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import FlowSettingsModal from '../components/flows/FlowSettingsModal'
import type { Flow } from '../lib/flows'
import type { ChannelLine } from '../lib/channelLines'

const lines: ChannelLine[] = [
  { id: 'l-waha', provider: 'waha', provider_kind: 'unofficial', label: 'Atendimento WAHA', number: '+5592999990000', status: 'active', can_send_text: true, window_required: false },
  { id: 'l-meta', provider: 'meta_cloud', provider_kind: 'official', label: 'K3G Oficial', number: '+559284517378', status: 'active', can_send_text: true, window_required: true },
  { id: 'l-meta2', provider: 'meta_cloud', provider_kind: 'official', label: 'Segundo oficial', status: 'active', can_send_text: true, window_required: true },
]

function flow(over: Partial<Flow['trigger_filter']> = {}): Flow {
  return { id: 'f', name: 'Recepção', priority: 999, is_default: true, restart_policy: 'new_conversation_only', trigger_filter: over } as Flow
}

function setup(f: Flow) {
  const onSave = vi.fn()
  render(<FlowSettingsModal open flow={f} lines={lines} onClose={vi.fn()} onSave={onSave} />)
  return onSave
}

describe('FlowSettingsModal — número de entrada', () => {
  it('sem filtro atende todos os números e não lista as linhas', () => {
    setup(flow())
    expect(screen.getByLabelText('Todos os números')).toBeChecked()
    expect(screen.queryByLabelText(/K3G Oficial/)).not.toBeInTheDocument()
  })

  it('permite escolher um único número, ou vários', async () => {
    const user = userEvent.setup()
    const onSave = setup(flow())
    await user.click(screen.getByLabelText('Só nos números marcados'))
    await user.click(screen.getByLabelText(/K3G Oficial/))
    await user.click(screen.getByRole('button', { name: 'Salvar' }))
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ connection_ids: ['l-meta'] }))
    await user.click(screen.getByLabelText(/Atendimento WAHA/))
    await user.click(screen.getByRole('button', { name: 'Salvar' }))
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ connection_ids: ['l-meta', 'l-waha'] }))
  })

  it('mostra o filtro salvo e marca quais linhas são oficiais', () => {
    setup(flow({ connection_ids: ['l-meta'] }))
    expect(screen.getByLabelText('Só nos números marcados')).toBeChecked()
    expect(screen.getByLabelText(/K3G Oficial/)).toBeChecked()
    expect(screen.getByLabelText(/Atendimento WAHA/)).not.toBeChecked()
    expect(screen.getAllByText('oficial').length).toBe(2)
    expect(screen.getByText('não oficial')).toBeInTheDocument()
  })

  it('desmarcar o último número não vira "todos" sozinho: bloqueia o salvar', async () => {
    const user = userEvent.setup()
    const onSave = setup(flow({ connection_ids: ['l-meta'] }))
    await user.click(screen.getByLabelText(/K3G Oficial/))
    expect(screen.getByLabelText('Só nos números marcados')).toBeChecked()
    expect(screen.getByRole('alert')).toHaveTextContent('Marque ao menos um número')
    expect(screen.getByRole('button', { name: 'Salvar' })).toBeDisabled()
    expect(onSave).not.toHaveBeenCalled()
  })

  it('voltar para "Todos os números" salva sem filtro de linha', async () => {
    const user = userEvent.setup()
    const onSave = setup(flow({ connection_ids: ['l-meta'] }))
    await user.click(screen.getByLabelText('Todos os números'))
    await user.click(screen.getByRole('button', { name: 'Salvar' }))
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ connection_ids: [] }))
  })
})
