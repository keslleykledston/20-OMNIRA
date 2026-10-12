// The attendances a person had open as tabs in the Hub inbox, remembered for the browser tab (sessionStorage) so a reload does not lose them.
// ONLY item ids are kept: no customer name, no company name. The names come back from the server when the tabs are restored, so an access that ended
// in the meantime brings nothing back (the server answers 404 and the tab is dropped). A sign-out forgets them (clearSession).
const PREFIX = 'omnira.hub.tabs.'
export const MAX_TABS = 8

export interface SavedTabs {
  ids: string[]
  selected: string
}

export function loadTabs(hubId: string): SavedTabs {
  try {
    const raw = sessionStorage.getItem(PREFIX + hubId)
    if (!raw) return { ids: [], selected: '' }
    const v = JSON.parse(raw) as Partial<SavedTabs>
    const ids = Array.isArray(v.ids) ? v.ids.filter((x): x is string => typeof x === 'string' && x.length > 0 && x.length <= 64).slice(-MAX_TABS) : []
    return { ids, selected: typeof v.selected === 'string' && ids.includes(v.selected) ? v.selected : '' }
  } catch {
    return { ids: [], selected: '' }
  }
}

export function saveTabs(hubId: string, saved: SavedTabs): void {
  try {
    if (saved.ids.length === 0) sessionStorage.removeItem(PREFIX + hubId)
    else sessionStorage.setItem(PREFIX + hubId, JSON.stringify({ ids: saved.ids.slice(-MAX_TABS), selected: saved.selected }))
  } catch {
    // storage unavailable (private window, blocked): tabs simply do not survive a reload
  }
}

export function forgetAllTabs(): void {
  try {
    for (let i = sessionStorage.length - 1; i >= 0; i--) {
      const k = sessionStorage.key(i)
      if (k && k.startsWith(PREFIX)) sessionStorage.removeItem(k)
    }
  } catch {
    // nothing to forget
  }
}
