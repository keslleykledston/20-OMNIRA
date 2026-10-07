import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import MessageComposer from '../components/inbox/MessageComposer'
import { Drawer } from '../components/primitives'

function Switcher() {
  const [id, setId] = useState('c1')
  return (
    <>
      <button onClick={() => setId(id === 'c1' ? 'c2' : 'c1')}>trocar</button>
      <MessageComposer draftKey={`t:${id}`} onSend={vi.fn(async () => true)} />
    </>
  )
}

describe('Composer drafts belong to their conversation', () => {
  it('never carries the text of one attendance into another, and brings it back when returning', async () => {
    render(<Switcher />)
    const box = () => screen.getByPlaceholderText('Escreva uma mensagem...') as HTMLTextAreaElement
    fireEvent.change(box(), { target: { value: 'rascunho da Ana' } })
    fireEvent.click(screen.getByText('trocar'))
    expect(box().value).toBe('') // the other person starts clean
    fireEvent.change(box(), { target: { value: 'rascunho do Bruno' } })
    fireEvent.click(screen.getByText('trocar'))
    expect(box().value).toBe('rascunho da Ana') // and Ana's text is still there
  })

  it('clears only the draft that was sent', async () => {
    const user = userEvent.setup()
    render(<Switcher />)
    const box = () => screen.getByPlaceholderText('Escreva uma mensagem...') as HTMLTextAreaElement
    await user.type(box(), 'enviar isto')
    await user.click(screen.getByRole('button', { name: 'Enviar mensagem' }))
    expect(box().value).toBe('')
  })
})

describe('Drawer', () => {
  it('closes with Escape and returns the focus to what opened it; clicks outside close it too', async () => {
    const user = userEvent.setup()
    function Host() {
      const [open, setOpen] = useState(false)
      return (
        <>
          <button onClick={() => setOpen(true)}>abrir</button>
          <Drawer open={open} title="Detalhes do atendimento" onClose={() => setOpen(false)}>
            <button>dentro</button>
          </Drawer>
        </>
      )
    }
    render(<Host />)
    const trigger = screen.getByText('abrir')
    await user.click(trigger)
    expect(screen.getByRole('dialog', { name: 'Detalhes do atendimento' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(trigger).toHaveFocus()
    await user.click(trigger)
    fireEvent.click(screen.getByRole('dialog').parentElement!) // the overlay
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
