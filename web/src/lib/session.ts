// Session values for the M05 inbox pages. The app stores its JWT under "token"
// (lib/store.ts); "jwtToken" is kept as a fallback for the earlier M05 pages.
export function getAuthToken(): string {
  return localStorage.getItem('token') || localStorage.getItem('jwtToken') || '';
}

export function authHeaders(): Record<string, string> {
  const token = getAuthToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}
