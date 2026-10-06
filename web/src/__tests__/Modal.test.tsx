import { describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Modal } from '../components/primitives'

// Regressão: um `onClose` inline muda de identidade a cada render; o foco inicial não pode ser refeito por isso.
function Host({ onEscape }: { onEscape?: () => void }) {
  const [open, setOpen] = useState(true)
  const [text, setText] = useState('')
  return (
    <>
      <p data-testid="open">{String(open)}</p>
      <Modal open={open} title="Nota" onClose={() => { onEscape?.(); setOpen(false) }}>
        <input aria-label="Texto" value={text} onChange={(e) => setText(e.target.value)} />
      </Modal>
    </>
  )
}

describe('Modal', () => {
  it('keeps the focus on the field while typing, even when onClose is an inline callback', async () => {
    const user = userEvent.setup()
    render(<Host />)
    await user.type(screen.getByLabelText('Texto'), 'texto longo digitado')
    expect(screen.getByLabelText('Texto')).toHaveValue('texto longo digitado')
    expect(screen.getByLabelText('Texto')).toHaveFocus()
  })

  it('still closes on Escape using the latest callback', async () => {
    const onEscape = vi.fn()
    const user = userEvent.setup()
    render(<Host onEscape={onEscape} />)
    await user.type(screen.getByLabelText('Texto'), 'ab')
    await user.keyboard('{Escape}')
    expect(onEscape).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('open')).toHaveTextContent('false')
  })

  it('moves focus into the dialog when it opens', () => {
    render(<Host />)
    expect(screen.getByRole('dialog')).toContainElement(document.activeElement as HTMLElement)
  })
})
