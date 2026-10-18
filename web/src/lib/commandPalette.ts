// Command palette (⌘K / Ctrl+K): the pure rules. The screen is components/CommandPalette.tsx.

/** Lower case without accents, so "configuracoes" finds "Configurações". */
export function fold(text: string): string {
  return text.normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLowerCase().trim()
}

/** Every word of the query has to appear in the text (any order); an empty query matches everything. */
export function matchesQuery(text: string, query: string): boolean {
  const words = fold(query).split(/\s+/).filter(Boolean)
  if (words.length === 0) return true
  const hay = fold(text)
  return words.every((w) => hay.includes(w))
}

/** ⌘K on macOS, Ctrl+K elsewhere. Shift/Alt variants are other shortcuts and are left alone. */
export function isPaletteShortcut(e: Pick<KeyboardEvent, 'key' | 'metaKey' | 'ctrlKey' | 'shiftKey' | 'altKey'>): boolean {
  return e.key.toLowerCase() === 'k' && (e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey
}

/** Remote search starts at two characters: one letter would list the whole company. */
export const MIN_REMOTE_QUERY = 2

/** Moves the highlighted row, wrapping at both ends. With no rows there is nothing to highlight. */
export function moveActive(current: number, delta: 1 | -1, count: number): number {
  if (count <= 0) return 0
  return (current + delta + count) % count
}
