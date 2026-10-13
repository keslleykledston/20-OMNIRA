// Where to send a person once the sign-in is done. The console (/admin/) is a separate entry of the same SPA build and shares the sign-in page of the
// agent app; before leaving for it, the console remembers the address it wanted. Only addresses inside the console are ever accepted back (never an
// arbitrary URL from storage), so this cannot become an open redirect.
const KEY = 'omnira.afterLogin'

export function isConsolePath(p: string): boolean {
  return /^\/admin(\/|\?|$)/.test(p) && !p.includes('\\')
}

export function rememberAfterLogin(path: string): void {
  if (!isConsolePath(path)) return
  try {
    sessionStorage.setItem(KEY, path)
  } catch {
    /* storage blocked: the person lands on the default screen */
  }
}

// Returns the remembered console address once (and forgets it); null when there is none or it is not a console address.
export function takeAfterLogin(): string | null {
  try {
    const p = sessionStorage.getItem(KEY)
    sessionStorage.removeItem(KEY)
    return p && isConsolePath(p) ? p : null
  } catch {
    return null
  }
}
