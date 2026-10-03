// POST /auth/logout answers 200 {"end_session_url": ...} when the identity
// provider has its own session to end, 204 otherwise. The address is followed by
// the browser, so only an absolute http(s) URL is accepted: anything else
// (javascript:, a relative path, a non-string) is ignored and the user simply
// lands on /login as before.
export function endSessionUrlFrom(data: unknown): string | undefined {
  const raw = (data as { end_session_url?: unknown } | null | undefined)?.end_session_url
  if (typeof raw !== 'string') return undefined
  try {
    const u = new URL(raw)
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.toString() : undefined
  } catch {
    return undefined
  }
}
