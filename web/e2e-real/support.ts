import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
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

/**
 * These helpers write with the database owner's rights (sessions, seed data, hubctl). They run ONLY against the throwaway database that
 * scripts/e2e-hub-browser.sh creates (Codex review). The script hands over ONE value, the run number; everything else is DERIVED and proven:
 * the container is named from it AND carries the script's own run label and image; the database URL is built from that container's real
 * published port (so SQL and hubctl reach the same database by construction); the hubctl binary must be a regular file, not a link, owned by
 * this user, not writable by others, inside this run's private work directory. No shell is involved anywhere, and the seeded database is
 * checked (five e2e.test people and nobody else) before EVERY write.
 */
function target(): { container: string; appdb: string; hubctlBin: string } {
  const run = process.env.E2E_RUN ?? '';
  if (!/^\d{1,10}$/.test(run)) throw new Error('E2E_RUN is not a run number of scripts/e2e-hub-browser.sh: refusing to write anywhere');
  const nonce = process.env.E2E_NONCE ?? '';
  const pgId = process.env.E2E_PG_ID ?? '';
  if (!/^[0-9a-f]{32}$/.test(nonce) || !/^[0-9a-f]{64}$/.test(pgId)) throw new Error('E2E_NONCE / E2E_PG_ID are not the run markers of scripts/e2e-hub-browser.sh');
  const name = `omnira-hubbrowser-${run}-pg`;
  // the IMMUTABLE container id the script recorded must be the one behind the name NOW (checked on every call: a replaced container fails closed)
  const identity = execFileSync('docker', ['inspect', '-f', '{{.Id}}|{{index .Config.Labels "com.omnira.integration-test.run"}}|{{.Config.Image}}', name], { encoding: 'utf8' }).trim();
  if (identity !== `${pgId}|hubbrowser-${run}|postgres:16-alpine`) throw new Error(`the container ${name} is not the throwaway database of this run (${identity})`);
  const container = pgId; // from here on the container is addressed by its immutable id, never by name
  const port = execFileSync('docker', ['port', container, '5432/tcp'], { encoding: 'utf8' }).trim().split('\n')[0].split(':').pop() ?? '';
  if (!/^\d{2,5}$/.test(port)) throw new Error('cannot determine the published port of the throwaway database');
  const appdb = `postgres://omnira_app:omnira_app@127.0.0.1:${port}/hubbrowser?sslmode=disable`;

  const work = process.env.E2E_WORK ?? '';
  const realWork = fs.realpathSync(work);
  if (!work || realWork !== work || path.dirname(work) !== fs.realpathSync(os.tmpdir())) throw new Error('E2E_WORK is not this run\'s private temporary directory');
  const wst = fs.lstatSync(work);
  if (!wst.isDirectory() || wst.uid !== process.getuid!() || (wst.mode & 0o077) !== 0) throw new Error('the work directory must be private to this user');
  const hubctlBin = path.join(work, 'bin', 'hubctl');
  const bst = fs.lstatSync(hubctlBin);
  if (!bst.isFile() || bst.isSymbolicLink() || bst.uid !== process.getuid!() || (bst.mode & 0o022) !== 0) throw new Error('hubctl must be a regular file of this user, not writable by others');

  // the database itself must carry the marker only this run wrote (and hold exactly the five seeded e2e.test people): checked every call
  const probe = psql(container, `SELECT current_database() || '|' || coalesce(current_setting('omnira.e2e_run', true), '') || '|' || (SELECT count(*) FROM users) || '|' || (SELECT count(*) FROM users WHERE email LIKE '%@e2e.test')`);
  if (probe !== `hubbrowser|${nonce}|5|5`) throw new Error(`the database is not the seeded throwaway one of this run (${probe.replace(nonce, '<nonce>')}): refusing to write`);
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
