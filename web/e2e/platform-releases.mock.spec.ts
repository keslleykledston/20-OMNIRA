import { test, expect, Page, Route } from '@playwright/test';

// "Administração global → Plataforma → Versões e Atualizações" (F7) em navegador real, com a API MOCKADA — o backend
// (internal/platformrelease/updatemgr) é provado por 20 testes de integração reais contra PostgreSQL + o ledger SQLite real
// do Update Agent (F6, docs/release/IMPLEMENTATION-STATUS.md). Aqui se confere a tela: o que um operador de plataforma vê e
// pode fazer, e que quem NÃO é operador nunca vê nem consegue acionar nada, mesmo manipulando a requisição diretamente.

const RELEASE_ID = 'rel-2026.10.0';
const manifest = () => ({
  schema_version: 1,
  release_id: RELEASE_ID,
  platform_version: '2026.10.0',
  channel: 'stable',
  source: { repository: 'omnira/omnira', commit_sha: 'abc123', workflow_run_id: '42' },
  artifacts: [{ unit: 'api', image: 'ghcr.io/omnira/api:2026.10.0' }],
  modules_changed: ['platformrelease'],
  compatibility: { from_versions: ['2026.09.0'], min_agent_protocol: 1, database_strategy: 'expand_only' },
  migrations: { start_after: '000172', target: '000173', checksum_bundle: 'sha256:deadbeef' },
  rollout: { workloads: ['api'], preflight_policy: 'strict', recovery_class: 'manual' },
  approved_by: 'dono',
});

async function install(page: Page, opts: { operator: boolean }) {
  const releases = [{ release_id: RELEASE_ID, channel: 'stable', platform_version: '2026.10.0', manifest: manifest(), ingested_by: 'op-1', ingested_at: '2026-10-20T10:00:00Z' }];
  const runs: { id: string; plan_id: string; release_id: string; state: string; idempotency_key: string; actor_id: string; created_at: string; updated_at: string }[] = [];
  const events: { seq: number; kind: string; detail: unknown; created_at: string }[] = [];
  const writes: { method: string; path: string; body: unknown }[] = [];
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

  await page.addInitScript(() => {
    localStorage.setItem('token', 'mock-token');
    localStorage.setItem('sessionActive', 'true');
    localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'operador@k3g.example', name: 'Operador' }));
  });

  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname.replace('/api/v1', '');
    const method = route.request().method();

    if (p === '/hubs') return json(route, { items: [] }); // no Hub grant at all: a pure platform operator
    if (!opts.operator) return json(route, 'not found', 404); // uniform 404 — the SAME answer for any /platform/* route

    if (p === '/platform/installation' && method === 'GET') {
      return json(route, { channel: 'stable', agent_protocol: 1, current_version: '2026.09.0', current_release_id: 'rel-2026.09.0', current_digests: {}, updated_at: '2026-10-20T09:00:00Z' });
    }
    if (p === '/platform/releases' && method === 'GET') return json(route, { items: releases });
    if (p === '/platform/releases' && method === 'POST') {
      const body = route.request().postDataJSON();
      writes.push({ method, path: p, body });
      return json(route, { release_id: body.release_id, channel: body.channel, platform_version: body.platform_version, manifest: body, ingested_by: 'op-1', ingested_at: new Date().toISOString() }, 201);
    }
    if (p === '/platform/updates/plans' && method === 'POST') {
      const body = route.request().postDataJSON();
      writes.push({ method, path: p, body });
      return json(
        route,
        {
          id: 'plan-1', release_id: body.release_id, preflight_status: 'PASS',
          preflight_detail: { version_known: true, version_compatible: true, agent_protocol_ok: true, reasons: [] },
          created_by: 'op-1', created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 900_000).toISOString(),
        },
        201,
      );
    }
    if (p === '/platform/updates/runs' && method === 'POST') {
      const body = route.request().postDataJSON();
      writes.push({ method, path: p, body });
      const run = { id: 'run-1', plan_id: body.plan_id, release_id: RELEASE_ID, state: 'DEPLOYING', idempotency_key: 'k', actor_id: 'op-1', created_at: new Date().toISOString(), updated_at: new Date().toISOString() };
      runs.push(run);
      events.push({ seq: 0, kind: 'run.locked', detail: {}, created_at: new Date().toISOString() });
      return json(route, run, 201);
    }
    if (p === '/platform/updates/runs' && method === 'GET') return json(route, { items: runs });
    const runMatch = p.match(/^\/platform\/updates\/runs\/([^/]+)$/);
    if (runMatch && method === 'GET') {
      const found = runs.find((r) => r.id === runMatch[1]);
      return found ? json(route, found) : json(route, 'not found', 404);
    }
    const eventsMatch = p.match(/^\/platform\/updates\/runs\/([^/]+)\/events$/);
    if (eventsMatch && method === 'GET') {
      const after = Number(url.searchParams.get('after') ?? -1);
      return json(route, { items: events.filter((e) => e.seq > after) });
    }
    return json(route, 'not found', 404);
  });

  return { writes, runs };
}

test('um operador de plataforma roda o preflight, confirma e acompanha a atualização até o histórico', async ({ page }) => {
  const state = await install(page, { operator: true });
  await page.goto('/admin/plataforma');

  await expect(page.getByText('Versões e atualizações')).toBeVisible();
  await expect(page.getByText('2026.09.0')).toBeVisible();
  await expect(page.getByText(RELEASE_ID)).toBeVisible();

  await page.getByRole('button', { name: 'Detalhes' }).click();
  await page.getByRole('button', { name: 'Rodar preflight' }).click();
  await expect(page.getByText('PASS')).toBeVisible();

  await page.getByRole('button', { name: 'Iniciar atualização (escopo global)' }).click();
  const dialog = page.getByRole('dialog', { name: 'Confirmar atualização de escopo global' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText(/atualiza a instalação OMNIRA inteira/)).toBeVisible();
  await dialog.getByRole('button', { name: 'Iniciar atualização' }).click();

  await expect(page.getByText('Acompanhando run-1')).toBeVisible();
  await expect(page.getByText('run.locked')).toBeVisible();
  expect(state.writes.some((w) => w.path === '/platform/updates/runs')).toBe(true);

  // Reentrada: recarregar a tela enquanto o run está em andamento volta direto ao acompanhamento, não ao catálogo.
  await page.reload();
  await expect(page.getByText('Acompanhando run-1')).toBeVisible();
});

test('quem não é operador de plataforma (e não administra Hub nenhum) nunca vê a tela, mesmo navegando direto para o endereço', async ({ page }) => {
  await install(page, { operator: false });
  await page.goto('/admin/plataforma');
  // AdminShell já recusa antes da página existir: a mesma guarda que protege /acessos e /instancias.
  await expect(page.getByText('Sem acesso ao console')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Iniciar atualização (escopo global)' })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Plataforma' })).toHaveCount(0);
});

test('um administrador de Hub que NÃO é operador de plataforma chega à tela mas o servidor recusa tudo nela', async ({ page }) => {
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.addInitScript(() => {
    localStorage.setItem('token', 'mock-token');
    localStorage.setItem('sessionActive', 'true');
    localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'admin-hub@k3g.example', name: 'Admin do Hub' }));
  });
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname.replace('/api/v1', '');
    if (p === '/hubs') return json(route, { items: [{ id: 'hub-1', name: 'K3G', role: 'hub_admin', can_manage_access: true }] });
    // every /platform/* route answers the SAME uniform 404 — including ListReleases, which a forged
    // direct request (bypassing the UI entirely) would hit too: the server, not the screen, is the gate.
    return json(route, 'not found', 404);
  });
  await page.goto('/admin/plataforma');
  // Reached via the Hub-admin entitlement (the nav link is hidden, but react-router does not block a direct address),
  // yet the page's OWN probe (GetInstallation) is refused — never a visual-only gate, always a real server answer.
  await expect(page.getByText(/só para operadores de plataforma/)).toBeVisible();
  await expect(page.getByRole('link', { name: 'Plataforma' })).toHaveCount(0);
});
