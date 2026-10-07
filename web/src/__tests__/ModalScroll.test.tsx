import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { Modal } from '../components/primitives'

describe('Modal — a long body never hides the footer', () => {
  it('caps the panel to the screen and scrolls only the body, outside of which header and footer stay', () => {
    render(
      <Modal open title="Instalar pack" onClose={() => {}} footer={<button>Continuar</button>}>
        <ul>{Array.from({ length: 60 }, (_, i) => <li key={i}>item {i}</li>)}</ul>
      </Modal>,
    )
    const dialog = screen.getByRole('dialog')
    expect(dialog.className).toMatch(/max-h-\[calc\(100dvh-2rem\)\]/)
    expect(dialog.className).toContain('flex-col')
    const body = screen.getByText('item 0').closest('div')!
    expect(body.className).toContain('overflow-y-auto')
    // header and footer are siblings of the scrolling body, never inside it
    expect(body.contains(screen.getByRole('button', { name: 'Continuar' }))).toBe(false)
    expect(body.contains(screen.getByRole('button', { name: 'Fechar' }))).toBe(false)
  })
})
