import { test, expect, Page, Route } from '@playwright/test';

// Pessoas (one page for members, invitations, who attends and in which queues) in a real browser with the API mocked. The mock keeps
// state, so a change made in the person's sheet must show up after the screen reads the API again. PEOPLE_SHOTS=<dir> saves screenshots.

const TENANT = '11111111-1111-1111-1111-111111111111';
const SHOTS = process.env.PEOPLE_SHOTS;

function makeApi(page: Page) {
  const seat = (id: string, queue_id: string, queue_name: string, available = true, capacity = 2) => ({ id, queue_id, queue_name, available, capacity });
  const state = {
    calls: [] as { method: string; path: string; body: any }[],
    members: [
      { membership_id: 'm-ana', user_id: 'u-ana', name: 'Ana Ribeiro', email: 'ana.ribeiro@exemplo.com.br', role_key: 'tenant_admin', role_name: 'Tenant Administrator', status: 'active', created_at: '2026-10-01T00:00:00Z', last_login_at: new Date().toISOString() },
      { membership_id: 'm-bruno', user_id: 'u-bruno', name: 'Bruno Costa', email: 'bruno.costa@exemplo.com.br', role_key: 'tenant_agent', role_name: 'Tenant Agent', status: 'active', created_at: '2026-10-01T00:00:00Z', last_login_at: '2026-10-13T18:03:00Z' },
      { membership_id: 'm-carla', user_id: 'u-carla', name: 'Carla Mendes', email: 'carla.mendes@exemplo.com.br', role_key: 'tenant_agent', role_name: 'Tenant Agent', status: 'active', created_at: '2026-10-01T00:00:00Z' },
      { membership_id: 'm-diego', user_id: 'u-diego', name: 'Diego Alves', email: 'diego.alves@exemplo.com.br', role_key: 'tenant_supervisor', role_name: 'Tenant Supervisor', status: 'active', created_at: '2026-10-01T00:00:00Z' },
      { membership_id: 'm-fabio', user_id: 'u-fabio', name: 'Fábio Nunes', email: 'fabio.nunes@exemplo.com.br', role_key: 'tenant_agent', role_name: 'Tenant Agent', status: 'active', created_at: '2026-10-01T00:00:00Z' },
    ] as any[],
    invitations: [
      { id: 'i-edu', email: 'eduarda.lima@exemplo.com.br', role_key: 'tenant_agent', role_name: 'Tenant Agent', status: 'pending', created_by_email: 'ana.ribeiro@exemplo.com.br', expires_at: '2099-01-01T00:00:00Z', created_at: '2026-10-12T00:00:00Z', sent_at: '2026-10-12T00:01:00Z' },
    ] as any[],
    agents: [
      { id: 'p-ana', membership_id: 'm-ana', user_id: 'u-ana', name: 'Ana Ribeiro', email: 'ana.ribeiro@exemplo.com.br', role: 'Administrador', status: 'active', queues: [seat('qm-1', 'q-n1', 'Suporte N1', true, 5), seat('qm-2', 'q-fin', 'Financeiro', true, 3)] },
      { id: 'p-bruno', membership_id: 'm-bruno', user_id: 'u-bruno', name: 'Bruno Costa', email: 'bruno.costa@exemplo.com.br', role: 'Agente', status: 'active', queues: [seat('qm-3', 'q-n1', 'Suporte N1'), seat('qm-4', 'q-n2', 'Suporte N2', false)] },
      { id: 'p-carla', membership_id: 'm-carla', user_id: 'u-carla', name: 'Carla Mendes', email: 'carla.mendes@exemplo.com.br', role: 'Agente', status: 'active', queues: [] },
      { id: 'p-fabio', membership_id: 'm-fabio', user_id: 'u-fabio', name: 'Fábio Nunes', email: 'fabio.nunes@exemplo.com.br', role: 'Agente', status: 'disabled', queues: [seat('qm-5', 'q-com', 'Comercial')] },
    ] as any[],
    queues: [
      { id: 'q-n1', name: 'Suporte N1', mode: 'round_robin', is_default: true, member_count: 2, available_count: 2, open_conversation_count: 4, created_at: '', updated_at: '' },
      { id: 'q-n2', name: 'Suporte N2', mode: 'manual', is_default: false, member_count: 1, available_count: 0, open_conversation_count: 1, created_at: '', updated_at: '' },
      { id: 'q-fin', name: 'Financeiro', mode: 'least_loaded', is_default: false, member_count: 1, available_count: 1, open_conversation_count: 2, created_at: '', updated_at: '' },
      { id: 'q-com', name: 'Comercial', mode: 'round_robin', is_default: false, member_count: 0, available_count: 0, open_conversation_count: 0, created_at: '', updated_at: '' },
    ] as any[],
    sectors: [
      { id: 's-sup', name: 'Suporte', active: true, queues: [{ id: 'q-n1', name: 'Suporte N1' }, { id: 'q-n2', name: 'Suporte N2' }] },
      { id: 's-fin', name: 'Financeiro', active: true, queues: [{ id: 'q-fin', name: 'Financeiro' }] },
      { id: 's-com', name: 'Comercial', active: true, queues: [{ id: 'q-com', name: 'Comercial' }] },
    ] as any[],
  };
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const recount = () => {
    for (const q of state.queues) {
      const seats = state.agents.filter((a) => a.status === 'active').flatMap((a) => a.queues).filter((s: any) => s.queue_id === q.id);
      q.member_count = seats.length;
      q.available_count = seats.filter((s: any) => s.available).length;
    }
  };
  return {
    state,
    install: async () => {
      await page.addInitScript(([t]) => {
        localStorage.setItem('token', 'mock-token');
        localStorage.setItem('tenantId', t);
        localStorage.setItem('sessionActive', 'true');
        localStorage.setItem('user', JSON.stringify({ id: 'u-ana', email: 'ana.ribeiro@exemplo.com.br', name: 'Ana Ribeiro' }));
      }, [TENANT]);
      await page.route('**/api/v1/**', async (route) => {
        const req = route.request();
        const url = new URL(req.url());
        const p = url.pathname.replace('/api/v1/tenants/' + TENANT, '');
        const m = req.method();
        const body = req.postData() ? JSON.parse(req.postData()!) : undefined;
        if (m !== 'GET') state.calls.push({ method: m, path: p, body });
        if (p.endsWith('/events') || p.endsWith('/presence/events')) return route.abort();
        if (p.endsWith('/me/access')) return json(route, { role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage', 'agent.read', 'agent.manage', 'channel.manage'], invitation_delivery_available: true });
        if (m === 'GET' && p === '/team') return json(route, { items: state.members });
        if (m === 'GET' && p === '/team/invitations') return json(route, { items: state.invitations });
        if (m === 'GET' && p === '/roles') return json(route, { items: [
          { id: 'r1', key: 'tenant_admin', name: 'Tenant Administrator', permissions: [] },
          { id: 'r2', key: 'tenant_supervisor', name: 'Tenant Supervisor', permissions: [] },
          { id: 'r3', key: 'tenant_agent', name: 'Tenant Agent', permissions: [] },
        ] });
        if (m === 'GET' && p === '/agents/presence') return json(route, { online_agent_profile_ids: ['p-ana', 'p-carla'] });
        if (m === 'GET' && p === '/agents') return json(route, { items: state.agents });
        if (m === 'GET' && p === '/queues') { recount(); return json(route, { items: state.queues }); }
        if (m === 'GET' && p === '/queues/lines') return json(route, { items: [] });
        if (m === 'GET' && p === '/sectors') return json(route, { items: state.sectors });
        if (m === 'GET' && p === '/shifts') return json(route, { items: [] });
        if (m === 'GET' && p === '/calendars') return json(route, { items: [], default_timezone: 'America/Manaus' });
        if (m === 'GET' && /^\/queues\/[^/]+\/availability$/.test(p)) return json(route, { open: true, at: new Date().toISOString(), calendar_source: 'sector', timezone: 'America/Manaus' });
        if (m === 'POST' && p === '/agents') {
          const mem = state.members.find((x) => x.membership_id === body.membership_id);
          const a = { id: 'p-' + mem.user_id.slice(2), membership_id: mem.membership_id, user_id: mem.user_id, name: mem.name, email: mem.email, role: 'Agente', status: 'active', queues: [] };
          state.agents.push(a);
          return json(route, { id: a.id, status: 'active' }, 201);
        }
        const agentPath = p.match(/^\/agents\/([^/]+)(?:\/queues(?:\/([^/]+))?)?$/);
        if (agentPath) {
          const a = state.agents.find((x) => x.id === agentPath[1]);
          if (m === 'PATCH' && !p.includes('/queues')) { a.status = body.status; return route.fulfill({ status: 204 }); }
          if (m === 'POST') {
            const q = state.queues.find((x) => x.id === body.queue_id);
            const s = seat('qm-' + Math.random().toString(36).slice(2, 7), q.id, q.name, body.available, body.capacity);
            a.queues.push(s);
            return json(route, { id: s.id }, 201);
          }
          if (m === 'PATCH') { Object.assign(a.queues.find((s: any) => s.id === agentPath[2]), body); return route.fulfill({ status: 204 }); }
          if (m === 'DELETE') { a.queues = a.queues.filter((s: any) => s.id !== agentPath[2]); return route.fulfill({ status: 204 }); }
        }
        const teamPath = p.match(/^\/team\/(m-[^/]+)$/);
        if (teamPath && m === 'PATCH') {
          const mem = state.members.find((x) => x.membership_id === teamPath[1]);
          if (body.role_key) mem.role_key = body.role_key;
          if (body.status) mem.status = body.status;
          return json(route, mem);
        }
        if (m === 'POST' && p === '/team/invitations') {
          const inv = { id: 'i-' + state.invitations.length, email: body.email, role_key: body.role_key, role_name: '', status: 'pending', created_by_email: 'ana.ribeiro@exemplo.com.br', expires_at: '2099-01-01T00:00:00Z', created_at: new Date().toISOString(), sent_at: new Date().toISOString() };
          state.invitations.push(inv);
          return json(route, inv, 201);
        }
        return json(route, { error: 'not mocked' }, 404);
      });
    },
  };
}

test('Pessoas: lista, ficha, filas e convite num navegador real', async ({ page }) => {
  const api = makeApi(page);
  await api.install();
  await page.goto('/settings/people');

  const carla = page.getByRole('button', { name: 'Abrir a ficha de Carla Mendes' });
  await expect(carla).toContainText('Sem fila');
  await expect(page.getByRole('button', { name: /Precisam de atenção/ })).toContainText('1');
  await expect(page.getByRole('button', { name: 'Abrir a ficha de eduarda.lima@exemplo.com.br' })).toContainText('Convite enviado');
  if (SHOTS) await page.screenshot({ animations: 'disabled', path: `${SHOTS}/pessoas-lista-desktop.png`, fullPage: true });

  // Carla has no queue: open her sheet, join Suporte N1 and see the list say she receives conversations
  await carla.click();
  const sheet = page.getByRole('dialog', { name: 'Ficha de Carla Mendes' });
  await expect(sheet.getByText('Carla ainda não recebe conversas')).toBeVisible();
  // the overlay covers the whole screen (a parent's space-y-* margin used to push it 20px down)
  expect(await page.evaluate(() => document.querySelector('[role="dialog"]')!.parentElement!.getBoundingClientRect().top)).toBe(0);
  if (SHOTS) await page.screenshot({ animations: 'disabled', path: `${SHOTS}/pessoas-ficha-sem-fila.png` });
  // choices are pending until "Salvar alterações" and its confirmation (owner, 2026-10-14)
  await sheet.getByLabel('Participa da fila Suporte N1').click();
  await expect(sheet.getByLabel('Participa da fila Suporte N1')).toBeChecked();
  await sheet.getByRole('button', { name: 'Aumentar: Capacidade na fila Suporte N1' }).click();
  await expect(sheet.getByRole('group', { name: 'Capacidade na fila Suporte N1' })).toContainText('2');
  await expect(sheet.getByText('1 alteração não salva')).toBeVisible();
  expect(api.state.calls.filter((c) => c.path.startsWith('/agents/p-carla'))).toEqual([]);
  if (SHOTS) await page.screenshot({ animations: 'disabled', path: `${SHOTS}/pessoas-ficha-pendente.png` });
  await sheet.getByRole('button', { name: 'Salvar alterações' }).click();
  const confirm = sheet.getByRole('alertdialog', { name: 'Aplicar as alterações em Carla Mendes?' });
  await expect(confirm).toContainText('Entra na fila Suporte N1: disponível, até 2 conversas ao mesmo tempo');
  if (SHOTS) await page.screenshot({ animations: 'disabled', path: `${SHOTS}/pessoas-ficha-confirmar.png` });
  await confirm.getByRole('button', { name: 'Aplicar alterações' }).click();
  await expect(sheet.getByText('Alterações salvas.')).toBeVisible();
  await expect(sheet.getByText('Carla recebe conversas')).toBeVisible();
  expect(api.state.calls).toContainEqual({ method: 'POST', path: '/agents/p-carla/queues', body: { queue_id: 'q-n1', available: true, capacity: 2 } });
  // no limit: 0 shows "sem limite"
  const less = sheet.getByRole('button', { name: 'Diminuir: Capacidade na fila Suporte N1' });
  await less.click();
  await less.click();
  await expect(sheet.getByRole('group', { name: 'Capacidade na fila Suporte N1' })).toContainText('sem limite');
  if (SHOTS) await page.screenshot({ animations: 'disabled', path: `${SHOTS}/pessoas-ficha-sem-limite.png` });
  // closing with a pending change asks first
  await page.keyboard.press('Escape');
  await sheet.getByRole('alertdialog', { name: 'Fechar sem salvar?' }).getByRole('button', { name: 'Fechar sem salvar' }).click();
  await expect(sheet).toBeHidden();
  await expect(carla).toContainText('Recebe conversas');
  await expect(carla).toContainText('Suporte N1');

  // Diego (supervisor) does not attend: turn attendance on from his sheet, choose a queue, save
  await page.getByRole('button', { name: 'Abrir a ficha de Diego Alves' }).click();
  const diego = page.getByRole('dialog', { name: 'Ficha de Diego Alves' });
  await diego.getByRole('button', { name: 'Passar a atender' }).click();
  await diego.getByLabel('Participa da fila Comercial').click();
  await diego.getByRole('button', { name: 'Salvar alterações' }).click();
  await diego.getByRole('alertdialog', { name: /^Aplicar as alterações/ }).getByRole('button', { name: 'Aplicar alterações' }).click();
  await expect(diego.getByText('Alterações salvas.')).toBeVisible();
  await expect(diego.getByLabel('Participa da fila Comercial')).toBeChecked();
  await page.keyboard.press('Escape');

  // invite a new person as supervisor
  await page.getByRole('button', { name: 'Adicionar pessoa' }).click();
  const invite = page.getByRole('dialog', { name: 'Adicionar pessoa' });
  await invite.getByLabel('E-mail').fill('gustavo.prado@exemplo.com.br');
  await invite.getByRole('radio', { name: /Supervisor/ }).click();
  await expect(invite.getByRole('radio', { name: /Supervisor/ })).toBeChecked();
  // the highlighted card follows the chosen function (its colours fade in, so a screenshot taken at once may still show the old one)
  await expect(invite.locator('label', { hasText: 'Supervisor' })).toHaveClass(/border-accent-primary/);
  await expect(invite.locator('label', { hasText: 'Agente' })).not.toHaveClass(/border-accent-primary/);
  if (SHOTS) await page.screenshot({ animations: 'disabled', path: `${SHOTS}/pessoas-convite.png` });
  await invite.getByRole('button', { name: 'Enviar convite' }).click();
  await expect(page.getByText('Convite enviado para gustavo.prado@exemplo.com.br.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Abrir a ficha de gustavo.prado@exemplo.com.br' })).toContainText('Convite enviado');

  // the same link seen from the queue
  await page.goto('/settings/attendance/sectors');
  await expect(page.getByRole('list', { name: 'Filas de Suporte' })).toContainText('Suporte N1');
  if (SHOTS) await page.screenshot({ animations: 'disabled', path: `${SHOTS}/filas-e-setores.png`, fullPage: true });
  await page.getByRole('button', { name: 'Pessoas da fila Suporte N2' }).click();
  const people = page.getByRole('dialog', { name: 'Pessoas da fila Suporte N2' });
  await expect(people.getByLabel('Bruno Costa participa da fila Suporte N2')).toBeChecked();
  await expect(people.getByLabel('Fábio Nunes participa da fila Suporte N2')).toBeDisabled();
});

test('Pessoas: no celular a lista vira cartões e a ficha ocupa a tela', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const api = makeApi(page);
  await api.install();
  await page.goto('/settings/people');
  await expect(page.getByRole('button', { name: 'Abrir a ficha de Ana Ribeiro' })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
  if (SHOTS) await page.screenshot({ animations: 'disabled', path: `${SHOTS}/pessoas-lista-celular.png`, fullPage: true });
  await page.getByRole('button', { name: 'Abrir a ficha de Bruno Costa' }).click();
  await expect(page.getByRole('dialog', { name: 'Ficha de Bruno Costa' })).toBeVisible();
  if (SHOTS) await page.screenshot({ animations: 'disabled', path: `${SHOTS}/pessoas-ficha-celular.png` });
});
