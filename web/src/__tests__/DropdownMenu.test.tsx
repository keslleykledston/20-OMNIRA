import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DropdownMenu } from '../components/primitives'

describe('DropdownMenu keyboard', () => {
  it('moves the focus to the first usable item when it opens, and the arrows go on from there', async () => {
    const user = userEvent.setup()
    render(<DropdownMenu label="Mais" actions={[{ label: 'Bloqueada', onSelect: vi.fn(), disabled: true }, { label: 'Primeira', onSelect: vi.fn() }, { label: 'Segunda', onSelect: vi.fn() }]} />)
    await user.click(screen.getByRole('button', { name: 'Mais' }))
    expect(screen.getByRole('menuitem', { name: 'Primeira' })).toHaveFocus()
    await user.keyboard('{ArrowDown}')
    expect(screen.getByRole('menuitem', { name: 'Segunda' })).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Mais' })).toHaveFocus()
  })
})
