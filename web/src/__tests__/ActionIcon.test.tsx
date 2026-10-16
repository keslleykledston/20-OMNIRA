import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ActionIcon } from '../components/primitives'

describe('ActionIcon', () => {
  it('is named by what it does and carries a caption for hover and keyboard focus (hidden from the accessibility tree: the name is the label)', () => {
    const onClick = vi.fn()
    render(<ActionIcon icon="transfer" label="Transferir" onClick={onClick} />)
    const button = screen.getByRole('button', { name: 'Transferir' })
    expect(button).toHaveTextContent('')
    const caption = button.parentElement!.querySelector('[data-tooltip]')!
    expect(caption).toHaveTextContent('Transferir')
    expect(caption).toHaveAttribute('aria-hidden', 'true')
    // shown only on hover / focus-within (CSS classes: hidden until then)
    expect(caption.className).toMatch(/\bhidden\b/)
    expect(caption.className).toMatch(/group-hover:block/)
    expect(caption.className).toMatch(/group-focus-within:block/)
  })

  it('still clicks, and a disabled one does not', async () => {
    const onClick = vi.fn()
    const { rerender } = render(<ActionIcon icon="plus" label="Adicionar tarefa" onClick={onClick} />)
    await userEvent.click(screen.getByRole('button', { name: 'Adicionar tarefa' }))
    expect(onClick).toHaveBeenCalledTimes(1)
    rerender(<ActionIcon icon="plus" label="Adicionar tarefa" onClick={onClick} disabled />)
    await userEvent.click(screen.getByRole('button', { name: 'Adicionar tarefa' }))
    expect(onClick).toHaveBeenCalledTimes(1)
  })
})
