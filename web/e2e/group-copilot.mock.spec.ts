import { test, expect, Page, Route } from '@playwright/test';

// The group copilot panel (ADR-0050) in a real browser with the API MOCKED (page.route): the built SPA only, never a database, NATS or WAHA.
// Every number and sentence on the screen comes from the fixtures below; nothing here is real data. GROUP_COPILOT_SHOTS=<dir> saves screenshots.

const TENANT = '11111111-1111-1111-1111-111111111111';
const GROUP = 'aaaaaaaa-0000-0000-0000-000000000001';
const SHOTS = process.env.GROUP_COPILOT_SHOTS;

const shot = async (page: Page, name: string) => {
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: false });
};

function makeApi(page: Page, opts: { aiEnabled?: boolean; permissions?: string[] } = {}) {
  const now = '2026-11-18T14:00:00Z';
  const state = {
    ai: opts.aiEnabled ?? true,
    calls: [] as string[],
    items: [
      { id: 'i1', kind: 'request', tier: 'episodic', status: 'open', truth_level: 'ai_inferred', summary: 'Cliente pediu a segunda via do boleto de novembro.', actor_label: 'Rafael (cliente)', evidence: [{ quote: 'preciso da segunda via do boleto de novembro', at: '2026-11-17T13:20:00Z' }], pinned: true, relevance: 1.4, last_reinforced_at: now, source: 'live', version: 2, created_at: now, updated_at: now },
      { id: 'i2', kind: 'agreement', tier: 'episodic', status: 'open', truth_level: 'human_confirmed', summary: 'Reunião de acompanhamento toda segunda às 9h.', actor_label: 'Gestor', evidence: [], pinned: false, relevance: 1.1, last_reinforced_at: now, source: 'human', version: 1, created_at: now, updated_at: now },
      { id: 'i3', kind: 'handled_topic', tier: 'episodic', status: 'resolved', truth_level: 'said_in_group', summary: 'Troca do certificado digital foi concluída em 10/11.', actor_label: 'Felipe (interno)', evidence: [{ quote: 'certificado trocado, pode testar', at: '2026-11-10T16:00:00Z' }], pinned: false, relevance: 0.8, last_reinforced_at: now, source: 'import', version: 1, created_at: now, updated_at: now },
      { id: 'i4', kind: 'fact', tier: 'consolidated', status: 'open', truth_level: 'ai_inferred', summary: 'O contrato do grupo vence no fim do ano; renovação é decidida pelo diretor.', actor_label: 'Diretor', evidence: [], pinned: false, relevance: 0.6, last_reinforced_at: now, source: 'consolidation', version: 1, created_at: now, updated_at: now },
    ] as any[],
    imports: [
      { id: 'im1', status: 'extracted', format: 'whatsapp_zip', source_filename: 'Conversa do WhatsApp com Cobrança.zip', sha256: 'a'.repeat(64), size_bytes: 3_412_000, messages_total: 1842, messages_parsed: 1790, messages_rejected: 38, messages_duplicate: 14, rejected_reasons: { system: 30, deleted: 8 }, range_from: '2026-05-02T10:00:00Z', range_to: '2026-11-15T20:00:00Z', batches_total: 12, batches_ok: 12, batches_failed: 0, items_created: 41, coverage_complete: true, created_at: '2026-11-16T09:00:00Z', expires_at: '2027-02-14T09:00:00Z', raw_available: true, version: 1 },
      { id: 'im2', status: 'extracted', format: 'whatsapp_txt', source_filename: 'Conversa antiga.txt', sha256: 'b'.repeat(64), size_bytes: 812_000, messages_total: 600, messages_parsed: 590, messages_rejected: 10, messages_duplicate: 0, range_from: '2026-01-02T10:00:00Z', range_to: '2026-04-30T20:00:00Z', batches_total: 5, batches_ok: 4, batches_failed: 1, items_created: 9, coverage_complete: false, created_at: '2026-11-16T08:00:00Z', expires_at: '2027-02-14T08:00:00Z', raw_available: true, version: 1 },
      { id: 'im3', status: 'discarded', format: 'whatsapp_txt', source_filename: 'Teste.txt', sha256: 'c'.repeat(64), size_bytes: 1200, messages_total: 20, messages_parsed: 20, coverage_complete: true, created_at: '2026-08-01T09:00:00Z', expires_at: '2026-10-30T09:00:00Z', discarded_at: '2026-08-02T09:00:00Z', discard_reason: 'confirmed', raw_available: false, version: 2 },
    ] as any[],
    proposals: [
      { id: 'p1', memory_item_id: 'i1', kind: 'case', title: 'Enviar segunda via do boleto de novembro', description: 'Cliente Rafael pediu no grupo a segunda via do boleto de novembro. Gerar e enviar pelo canal combinado.', priority: 'high', due_at: '2026-11-21T12:00:00Z', proposed_by: 'ai', status: 'pending_approval', version: 1, created_at: now },
      { id: 'p2', memory_item_id: 'i3', kind: 'task', title: 'Conferir o certificado digital após a troca', description: '', priority: 'normal', proposed_by: 'human', status: 'created', created_task_id: '9f8e7d6c-0000-0000-0000-000000000001', version: 3, created_at: '2026-11-11T10:00:00Z' },
    ] as any[],
  };
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const permissions = opts.permissions ?? ['group.read', 'group.manage', 'group.ai.read', 'group.ai.manage', 'group.import', 'group.memory.erase', 'service.create'];
  return {
    state,
    install: async () => {
      await page.addInitScript((t) => {
        localStorage.setItem('token', 'mock-token');
        localStorage.setItem('tenantId', t);
        localStorage.setItem('sessionActive', 'true');
        localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'supervisor@example.test', name: 'Supervisor' }));
      }, TENANT);
      await page.route('**/api/v1/**', async (route) => {
        const req = route.request();
        const url = new URL(req.url());
        const p = url.pathname.replace('/api/v1/tenants/' + TENANT, '');
        const m = req.method();
        state.calls.push(`${m} ${p}`);
        if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
        if (p.endsWith('/me/access')) return json(route, { permissions, role_key: 'tenant_supervisor' });
        if (m === 'GET' && p === '/groups') return json(route, { items: [{ id: GROUP, name: 'Cobrança - Cliente Exemplo', enabled: true, last_message_at: '2026-11-18T13:58:00Z', last_message: { author_name: 'Rafael', from_me: false, message_type: 'text', preview: 'obrigado, fico no aguardo' } }] });
        if (m === 'GET' && p === `/groups/${GROUP}/messages`) {
          return json(route, { has_more: false, items: [
            { id: 'm2', author_name: 'Rafael', from_me: false, message_type: 'text', body: 'obrigado, fico no aguardo', sent_at: '2026-11-18T13:58:00Z' },
            { id: 'm1', author_name: 'Rafael', from_me: false, message_type: 'text', body: 'preciso da segunda via do boleto de novembro', sent_at: '2026-11-17T13:20:00Z' },
          ] });
        }
        const ai = `/groups/${GROUP}/ai`;
        if (m === 'GET' && p === `${ai}/memory`) {
          return json(route, { group: { id: GROUP, name: 'Cobrança - Cliente Exemplo', enabled: true, ai_enabled: state.ai }, items: state.items, count: state.items.length, state: { last_run_at: '2026-11-18T12:00:00Z', last_run_ok: true, last_consolidated_at: '2026-11-15T03:00:00Z', analyze_requested_at: state.calls.includes(`POST ${ai}/analyze`) ? '2026-11-18T14:05:00Z' : null, runs: 7, prompt_version: 'v1' } });
        }
        if (m === 'POST' && p === `${ai}/analyze`) return json(route, { requested: true }, 202);
        if (m === 'GET' && p === `${ai}/imports`) return json(route, { items: state.imports, count: state.imports.length });
        if (m === 'GET' && p === `${ai}/proposals`) return json(route, { items: state.proposals, count: state.proposals.length });
        if (m === 'PATCH' && p === ai) { state.ai = JSON.parse(req.postData() || '{}').enabled; return json(route, { id: GROUP, ai_enabled: state.ai, imports_discarded: 0 }); }
        return json(route, { error: 'not mocked' }, 404);
      });
    },
  };
}

async function open(page: Page) {
  await page.goto('/groups');
  await page.getByRole('button', { name: /Cobrança - Cliente Exemplo/ }).first().click();
  await expect(page.getByRole('button', { name: /^Copiloto/ })).toBeVisible({ timeout: 15_000 });
}

test.describe('Copiloto de grupo (ADR-0050)', () => {
  test('painel com memória, importações, propostas e ajustes', async ({ page }) => {
    const api = makeApi(page);
    await api.install();
    await open(page);
    await shot(page, '01-grupo-com-botao-copiloto');

    await page.getByRole('button', { name: /^Copiloto/ }).click();
    const panel = page.getByRole('dialog', { name: /Copiloto do grupo/ });
    await expect(panel.getByText('Cliente pediu a segunda via do boleto de novembro.')).toBeVisible();
    await shot(page, '02-memoria');
    await panel.getByRole('button', { name: /Ver evidência/ }).first().click();
    await expect(panel.getByText('preciso da segunda via do boleto de novembro')).toBeVisible();
    await shot(page, '03-memoria-evidencia-aberta');
    await panel.getByRole('button', { name: 'Analisar agora' }).click();
    await expect(panel.getByText(/Pedido de leitura enviado em/)).toBeVisible();
    await shot(page, '04-memoria-leitura-pedida');

    await panel.getByRole('tab', { name: /Importações/ }).click();
    await expect(panel.getByText('Todas as mensagens foram lidas').first()).toBeVisible();
    await expect(panel.getByText('Cobertura incompleta')).toBeVisible();
    await expect(panel.getByRole('button', { name: 'Descartar arquivo original' }).nth(1)).toBeDisabled();
    await shot(page, '05-importacoes-cobertura');

    await panel.getByRole('tab', { name: /Propostas/ }).click();
    await expect(panel.getByRole('button', { name: 'Aprovar e criar ocorrência' })).toBeVisible();
    await shot(page, '06-propostas');

    await panel.getByRole('tab', { name: 'Ajustes' }).click();
    await expect(panel.getByRole('switch', { name: 'IA neste grupo' })).toHaveAttribute('aria-checked', 'true');
    await shot(page, '07-ajustes-privacidade');
    await panel.getByRole('button', { name: 'Apagar toda a memória do grupo' }).click();
    await expect(page.getByLabel('Digite APAGAR para confirmar')).toBeVisible();
    await shot(page, '08-apagar-memoria-confirmacao');
  });

  test('IA desligada: aviso, memória legível e ações que precisam da IA desativadas', async ({ page }) => {
    const api = makeApi(page, { aiEnabled: false });
    await api.install();
    await open(page);
    await page.getByRole('button', { name: /^Copiloto/ }).click();
    const panel = page.getByRole('dialog', { name: /Copiloto do grupo/ });
    await expect(panel.getByText(/A IA está desligada neste grupo/)).toBeVisible();
    await expect(panel.getByRole('button', { name: 'Analisar agora' })).toBeDisabled();
    await shot(page, '09-ia-desligada');
    await panel.getByRole('tab', { name: 'Ajustes' }).click();
    await panel.getByRole('switch', { name: 'IA neste grupo' }).click();
    await expect(page.getByText('Ligar a IA neste grupo?')).toBeVisible();
    await shot(page, '10-ligar-ia-confirmacao');
  });

  test('somente leitura: ninguém vê ações de mudança', async ({ page }) => {
    const api = makeApi(page, { permissions: ['group.read', 'group.ai.read'] });
    await api.install();
    await open(page);
    await page.getByRole('button', { name: /^Copiloto/ }).click();
    const panel = page.getByRole('dialog', { name: /Copiloto do grupo/ });
    await expect(panel.getByText('Cliente pediu a segunda via do boleto de novembro.')).toBeVisible();
    await expect(panel.getByRole('button', { name: 'Editar' })).toHaveCount(0);
    await expect(panel.getByRole('button', { name: 'Analisar agora' })).toHaveCount(0);
    await shot(page, '11-somente-leitura');
  });

  test('celular: o painel ocupa a tela inteira', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const api = makeApi(page);
    await api.install();
    await open(page);
    await page.getByRole('button', { name: /^Copiloto/ }).click();
    const panel = page.getByRole('dialog', { name: /Copiloto do grupo/ });
    await expect(panel.getByText('Cliente pediu a segunda via do boleto de novembro.')).toBeVisible();
    await shot(page, '12-celular-memoria');
    await panel.getByRole('tab', { name: /Importações/ }).click();
    await shot(page, '13-celular-importacoes');
  });

  test('flag desligada (404): nenhum botão, nenhum erro', async ({ page }) => {
    await page.addInitScript((t) => {
      localStorage.setItem('token', 'mock-token');
      localStorage.setItem('tenantId', t);
      localStorage.setItem('sessionActive', 'true');
      localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'supervisor@example.test', name: 'Supervisor' }));
    }, TENANT);
    await page.route('**/api/v1/**', async (route) => {
      const p = new URL(route.request().url()).pathname.replace('/api/v1/tenants/' + TENANT, '');
      if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
      if (p.endsWith('/me/access')) return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ permissions: ['group.read', 'group.manage', 'group.ai.read'], role_key: 'x' }) });
      if (p === '/groups') return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ id: GROUP, name: 'Cobrança - Cliente Exemplo', enabled: true, last_message_at: null, last_message: null }] }) });
      if (p.includes('/messages')) return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], has_more: false }) });
      return route.fulfill({ status: 404, contentType: 'text/plain', body: 'not found' });
    });
    await page.goto('/groups');
    await page.getByRole('button', { name: /Cobrança - Cliente Exemplo/ }).first().click();
    await expect(page.getByText(/Somente leitura/)).toBeVisible();
    await expect(page.getByRole('button', { name: /Copiloto/ })).toHaveCount(0);
    await expect(page.getByRole('alert')).toHaveCount(0);
  });
});
