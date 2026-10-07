import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import Layout from '../components/Layout'
import { renderAt, setSession } from './testUtils'

vi.mock('../hooks/usePresenceHeartbeat', () => ({ usePresenceHeartbeat: () => {} }))

describe('Layout — the document never scrolls', () => {
  it('locks the document while the authenticated shell is mounted and releases it after', () => {
    setSession()
    const { unmount } = renderAt(<Layout />)
    expect(document.documentElement.classList.contains('app-shell-lock')).toBe(true)
    expect(screen.getAllByRole('main').length).toBeGreaterThan(0)
    unmount()
    expect(document.documentElement.classList.contains('app-shell-lock')).toBe(false)
  })
})
