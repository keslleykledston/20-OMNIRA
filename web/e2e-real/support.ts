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

let verified = false;

/**
 * These helpers write with the database owner's rights (sessions, seed data, hubctl). They run ONLY against the throwaway database that
 * scripts/e2e-hub-browser.sh creates, and refuse anything else (Codex review): the connection command must name that script's own container,
 * and the database must be the seeded one, holding nobody but the five seeded e2e.test people.
 */
function assertThrowawayDatabase(): void {
  if (verified) return;
  const cmd = process.env.E2E_PSQL ?? '';
  if (!/^docker exec -i omnira-hubbrowser-\d+-pg psql -U omnira -d hubbrowser /.test(cmd)) {
    throw new Error('E2E_PSQL is not the throwaway database of scripts/e2e-hub-browser.sh: refusing to write anywhere else');
  }
  if (!/^env OMNIRA_DATABASE_URL=postgres:\/\/omnira_app:omnira_app@127\.0\.0\.1:\d+\/hubbrowser\?sslmode=disable \S+\/hubctl --operator e2e$/.test(process.env.E2E_HUBCTL ?? '')) {
    throw new Error('E2E_HUBCTL does not point at the throwaway database: refusing to run');
  }
  const probe = execSync(cmd, {
    input: `SELECT current_database() || '|' || (SELECT count(*) FROM users) || '|' || (SELECT count(*) FROM users WHERE email LIKE '%@e2e.test')`,
    encoding: 'utf8',
  }).trim();
  if (probe !== 'hubbrowser|5|5') throw new Error(`the database is not the seeded throwaway one (${probe}): refusing to write`);
  verified = true;
}

/** One SQL statement against the throwaway database, as its owner (for setting up and for checking what really happened). */
export function sql(statement: string): string {
  assertThrowawayDatabase();
  return execSync(`${process.env.E2E_PSQL}`, { input: statement, encoding: 'utf8' }).trim(); // via stdin: no shell quoting of the statement
}

export function hubctl(args: string): string {
  assertThrowawayDatabase();
  return execSync(`${process.env.E2E_HUBCTL} ${args}`, { encoding: 'utf8' }).trim();
}

/** GETs a hub API path with the person's own session (the same cookie/headers the page uses). */
export async function api(page: Page, path: string): Promise<{ status: number; body: string }> {
  const res = await page.request.get(`/api/v1${path}`); // the context's cookies are the person's real session
  return { status: res.status(), body: await res.text() };
}
