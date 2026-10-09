import { execFileSync } from 'node:child_process';
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

const CONTAINER_RE = /^omnira-hubbrowser-\d+-pg$/;
const APPDB_RE = /^postgres:\/\/omnira_app:omnira_app@127\.0\.0\.1:\d+\/hubbrowser\?sslmode=disable$/;
const HUBCTL_RE = /^\/[A-Za-z0-9_./-]+\/bin\/hubctl$/;
let verifiedFor = '';

/**
 * These helpers write with the database owner's rights (sessions, seed data, hubctl). They run ONLY against the throwaway database that
 * scripts/e2e-hub-browser.sh creates (Codex review): no shell is involved (fixed argv, the script only supplies three validated values: the
 * container name, the application URL of that database and the hubctl binary), and every new combination is verified against the database
 * itself - it must be the seeded one, holding nobody but the five seeded e2e.test people - before the first write.
 */
function target(): { container: string; appdb: string; hubctlBin: string } {
  const container = process.env.E2E_PG_CONTAINER ?? '';
  const appdb = process.env.E2E_APPDB ?? '';
  const hubctlBin = process.env.E2E_HUBCTL_BIN ?? '';
  if (!CONTAINER_RE.test(container)) throw new Error('E2E_PG_CONTAINER is not the throwaway database of scripts/e2e-hub-browser.sh: refusing to write anywhere else');
  if (!APPDB_RE.test(appdb)) throw new Error('E2E_APPDB does not point at the throwaway database: refusing to run');
  if (!HUBCTL_RE.test(hubctlBin)) throw new Error('E2E_HUBCTL_BIN is not the hubctl built by the script: refusing to run');
  const key = `${container}|${appdb}|${hubctlBin}`;
  if (verifiedFor !== key) {
    const probe = psql(container, `SELECT current_database() || '|' || (SELECT count(*) FROM users) || '|' || (SELECT count(*) FROM users WHERE email LIKE '%@e2e.test')`);
    if (probe !== 'hubbrowser|5|5') throw new Error(`the database is not the seeded throwaway one (${probe}): refusing to write`);
    verifiedFor = key;
  }
  return { container, appdb, hubctlBin };
}

function psql(container: string, statement: string): string {
  return execFileSync('docker', ['exec', '-i', container, 'psql', '-U', 'omnira', '-d', 'hubbrowser', '-X', '-q', '-At', '-v', 'ON_ERROR_STOP=1'], { input: statement, encoding: 'utf8' }).trim(); // statement via stdin
}

/** One SQL statement against the throwaway database, as its owner (for setting up and for checking what really happened). */
export function sql(statement: string): string {
  return psql(target().container, statement);
}

/** Runs omnira-hubctl (one command, e.g. "reconcile") against the throwaway database. */
export function hubctl(args: string): string {
  const t = target();
  return execFileSync(t.hubctlBin, ['--operator', 'e2e', ...args.split(' ').filter(Boolean)], { env: { ...process.env, OMNIRA_DATABASE_URL: t.appdb }, encoding: 'utf8' }).trim();
}

/** GETs a hub API path with the person's own session (the same cookie/headers the page uses). */
export async function api(page: Page, path: string): Promise<{ status: number; body: string }> {
  const res = await page.request.get(`/api/v1${path}`); // the context's cookies are the person's real session
  return { status: res.status(), body: await res.text() };
}
