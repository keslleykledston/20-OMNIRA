import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import Layout from '../components/Layout'
import { renderAt, setSession } from './testUtils'

vi.mock('../hooks/usePresenceHeartbeat', () => ({ usePresenceHeartbeat: () => {} }))

describe('Layout — search palette', () => {
  beforeEach(() => {
    setSession()
  })

  it('Ctrl+K toggles the palette from any screen and the header button opens it', async () => {
    renderAt(<Layout />)
    expect(screen.queryByRole('dialog', { name: 'Buscar' })).not.toBeInTheDocument()
    await userEvent.keyboard('{Control>}k{/Control}')
    expect(screen.getByRole('dialog', { name: 'Buscar' })).toBeInTheDocument()
    await userEvent.keyboard('{Control>}k{/Control}')
    expect(screen.queryByRole('dialog', { name: 'Buscar' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /Buscar \(Ctrl\+K\)/ }))
    expect(screen.getByRole('dialog', { name: 'Buscar' })).toBeInTheDocument()
  })
})
