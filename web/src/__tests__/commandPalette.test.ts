import { describe, expect, it } from 'vitest'
import { fold, isPaletteShortcut, matchesQuery, moveActive } from '../lib/commandPalette'

const key = (over: Partial<KeyboardEvent> = {}) => ({ key: 'k', metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, ...over })

describe('command palette rules', () => {
  it('matches without accents or case, every word in any order', () => {
    expect(fold('Configurações')).toBe('configuracoes')
    expect(matchesQuery('Setores e horários', 'HORARIOS setores')).toBe(true)
    expect(matchesQuery('Setores e horários', 'horarios financeiro')).toBe(false)
    expect(matchesQuery('Tickets', '')).toBe(true)
  })

  it('opens on Ctrl+K and ⌘K only', () => {
    expect(isPaletteShortcut(key({ ctrlKey: true }))).toBe(true)
    expect(isPaletteShortcut(key({ metaKey: true, key: 'K' }))).toBe(true)
    expect(isPaletteShortcut(key())).toBe(false)
    expect(isPaletteShortcut(key({ ctrlKey: true, shiftKey: true }))).toBe(false)
    expect(isPaletteShortcut(key({ ctrlKey: true, key: 'j' }))).toBe(false)
  })

  it('moves the highlight around the list and survives an empty list', () => {
    expect(moveActive(0, 1, 3)).toBe(1)
    expect(moveActive(2, 1, 3)).toBe(0)
    expect(moveActive(0, -1, 3)).toBe(2)
    expect(moveActive(0, 1, 0)).toBe(0)
  })
})
