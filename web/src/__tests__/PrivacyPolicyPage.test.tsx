import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import PrivacyPolicyPage from '../pages/PrivacyPolicyPage'

describe('PrivacyPolicyPage', () => {
  it('is public and carries the controller contact and the deletion instructions', () => {
    render(<PrivacyPolicyPage />)
    expect(screen.getByRole('heading', { level: 1, name: 'Política de Privacidade' })).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: 'privacidade@k3gsolutions.com.br' }).length).toBeGreaterThan(0)
    expect(screen.getByRole('heading', { name: /Como pedir a exclusão/ })).toBeInTheDocument()
  })
})
