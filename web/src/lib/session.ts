// Session values for the inbox pages. The JWT lives under "token" (lib/store.ts);
// the tenant comes from the backend login response.
export const TOKEN_KEY = 'token';
export const TENANT_KEY = 'tenantId';
export const USER_KEY = 'user';

export function getAuthToken(): string {
  return localStorage.getItem(TOKEN_KEY) || localStorage.getItem('jwtToken') || '';
}

export function getTenantId(): string {
  return localStorage.getItem(TENANT_KEY) || '';
}

export function authHeaders(): Record<string, string> {
  const token = getAuthToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export function saveSession(token: string, tenantId: string | undefined, user: unknown): void {
  localStorage.setItem(TOKEN_KEY, token);
  if (tenantId) localStorage.setItem(TENANT_KEY, tenantId);
  localStorage.setItem(USER_KEY, JSON.stringify(user));
}

export function clearSession(): void {
  [TOKEN_KEY, TENANT_KEY, USER_KEY, 'jwtToken'].forEach((k) => localStorage.removeItem(k));
}

// A 401 from the API means the token is missing/expired (the backend keys are per process):
// drop the session and send the user to the login page.
export function handleUnauthorized(): void {
  clearSession();
  if (!window.location.pathname.includes('/login')) {
    window.location.replace('/login');
  }
}

export function isUnauthorized(err: unknown): boolean {
  return (err as { response?: { status?: number } })?.response?.status === 401;
}
