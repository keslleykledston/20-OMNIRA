import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import Sidebar from '../components/Sidebar'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

describe('Sidebar — aplicações externas (ADR-0048)', () => {
  it('has an "Integrações" entry that leads to the external applications page', () => {
    setSession()
    renderAt(<Sidebar />)
    const link = screen.getByRole('link', { name: /Integrações/ })
    expect(link).toHaveAttribute('href', '/settings/integrations/apps')
  })
})
