import { describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
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

  it('names the hours-and-queues configuration for what it is, not "Atendimento" (UX audit M2)', () => {
    setSession()
    renderAt(<Sidebar />)
    expect(screen.getByRole('link', { name: /Setores e horários/ })).toHaveAttribute('href', '/settings/attendance')
    expect(screen.queryByRole('link', { name: /^Atendimento$/ })).toBeNull()
  })

  it('groups the menu in three titled groups, in order, and no two items share an icon (UX audit M1)', () => {
    setSession()
    renderAt(<Sidebar />)
    const groups = screen.getAllByRole('group')
    expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual(['Operação', 'Automação e canais', 'Configuração'])
    const names = (g: HTMLElement) => within(g).getAllByRole('link').map((l) => l.getAttribute('title')).filter((t) => t !== 'Relatórios') // Relatórios is mock-backed: only in development
    expect(names(groups[0])).toEqual(['Dashboard', 'Conversas', 'Tickets', 'Reconciliação', 'Contatos', 'Supervisor'])
    expect(names(groups[1])).toEqual(['Automação', 'Canais', 'Grupos'])
    expect(names(groups[2])).toEqual(['Pessoas', 'Setores e horários', 'Integrações', 'Configurações'])
    const icons = screen.getAllByRole('link').map((l) => l.querySelector('svg path')?.getAttribute('d'))
    expect(icons.every(Boolean)).toBe(true)
    expect(new Set(icons).size).toBe(icons.length)
  })
})
