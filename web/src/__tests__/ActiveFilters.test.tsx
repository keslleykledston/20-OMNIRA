import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ActiveFilters } from '../components/primitives'

describe('ActiveFilters', () => {
  it('shows nothing when nothing narrows the list', () => {
    const { container } = render(<ActiveFilters filters={[]} onRemove={() => {}} onClear={() => {}} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows one removable chip per filter and a single clear-all', async () => {
    const user = userEvent.setup()
    const onRemove = vi.fn()
    const onClear = vi.fn()
    render(<ActiveFilters filters={[{ key: 'status', label: 'Situação', value: 'Aberto' }, { key: 'priority', label: 'Prioridade', value: 'Alta' }]} onRemove={onRemove} onClear={onClear} />)
    expect(screen.getByRole('group', { name: 'Filtros ativos' })).toHaveTextContent('Situação: Aberto')
    await user.click(screen.getByRole('button', { name: 'Remover filtro Prioridade: Alta' }))
    expect(onRemove).toHaveBeenCalledWith('priority')
    await user.click(screen.getByRole('button', { name: 'Limpar tudo' }))
    expect(onClear).toHaveBeenCalledTimes(1)
  })
})
