import { execSync } from 'node:child_process';
import { expect, type Page } from '@playwright/test';

// Helpers of the real-stack browser E2E (scripts/e2e-hub-browser.sh sets E2E_* and starts the API, the database and the web app).
export const env = {
  hub: process.env.E2E_HUB ?? '',
  ta: process.env.E2E_TA ?? '',
  tb: process.env.E2E_TB ?? '',
  tc: process.env.E2E_TC ?? '',
};

/**
 * Signs the person in with a REAL server-side session: the same `auth_sessions` row the login flows create, and the same cookie the API
 * reads. (The dev login only knows three hard-coded people; the sessions of the seeded ones are minted the same way it does, so every
 * role of the scenario can be a different, real user with its own memberships and grants.)
 */
export async function signIn(page: Page, email: string): Promise<{ id: string; email: string; name: string }> {
  const row = sql(`SELECT id || '|' || COALESCE(display_name, '') FROM users WHERE email = '${email.replace(/'/g, "''")}'`);
  expect(row, `no seeded user ${email}`).not.toBe('');
  const [id, name] = row.split('|');
  const session = `e2e-${crypto.randomUUID()}`;
  sql(`INSERT INTO auth_sessions(id, user_id, auth_method, expires_at) VALUES ('${session}', '${id}', 'dev', now() + interval '2 hours')`);
  await page.context().addCookies([{ name: 'omnira_session', value: session, url: process.env.E2E_BASE_URL! }]);
  const user = { id, email, name };
  await page.addInitScript((u) => {
    localStorage.setItem('user', JSON.stringify(u));
    localStorage.setItem('sessionActive', 'true');
  }, user);
  return user;
}

/** One SQL statement against the throwaway database, as its owner (for setting up and for checking what really happened). */
export function sql(statement: string): string {
  return execSync(`${process.env.E2E_PSQL}`, { input: statement, encoding: 'utf8' }).trim(); // via stdin: no shell quoting of the statement
}

export function hubctl(args: string): string {
  return execSync(`${process.env.E2E_HUBCTL} ${args}`, { encoding: 'utf8' }).trim();
}

/** GETs a hub API path with the person's own session (the same cookie/headers the page uses). */
export async function api(page: Page, path: string): Promise<{ status: number; body: string }> {
  const res = await page.request.get(`/api/v1${path}`); // the context's cookies are the person's real session
  return { status: res.status(), body: await res.text() };
}
