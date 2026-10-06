import { test, expect, Page, Route } from '@playwright/test';

// Finalizar atendimento (ADR-0020) em um navegador real, com a API mockada: ver o que ficou pendente do contato, concluir uma
// pendência, finalizar o atendimento (motivo, resumo e itens) e ver a conversa passar a "finalizada", sem composer.

const TENANT = '11111111-1111-1111-1111-111111111111';
const CONV = 'd0d0d0d0-0000-0000-0000-0000000000a1';

function makeApi(page: Page) {
  const now = '2026-10-06T12:00:00Z';
  const state = {
    status: 'open' as 'open' | 'closed',
    posts: [] as { path: string; body: any }[],
    listStatusParams: [] as (string | null)[],
    followUps: [
      { id: 'f-1', conversation_id: 'c-0', kind: 'promise', text: 'Ligar com o resultado da visita técnica', owner_user_id: null, due_at: '2020-01-01T12:00:00Z', status: 'open', truth: 'agent_confirmed', created_at: now, resolved_at: null, resolution_note: '' },
      { id: 'f-2', conversation_id: 'c-0', kind: 'pending', text: 'Enviar a segunda via da fatura', owner_user_id: null, due_at: null, status: 'open', truth: 'agent_confirmed', created_at: now, resolved_at: null, resolution_note: '' },
    ],
  };
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const conversation = () => ({ id: CONV, contact_name: 'Maria Souza', contact_phone: '+5511988887777', status: state.status, created_at: now, updated_at: now, last_message_at: now, last_message_direction: 'inbound', last_message_preview: 'preciso de ajuda', assigned_to_user_id: 'u1', contact_kind: 'customer', conversation_kind: 'customer_service', message_count: 2 });
  return {
    state,
    install: async () => {
      await page.addInitScript(([t]) => {
        localStorage.setItem('token', 'mock-token');
        localStorage.setItem('tenantId', t);
        localStorage.setItem('sessionActive', 'true');
        localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'agent@example.test', name: 'Atendente' }));
      }, [TENANT]);
      await page.route('**/api/v1/**', async (route) => {
        const req = route.request();
        const url = new URL(req.url());
        const p = url.pathname.replace('/api/v1/tenants/' + TENANT, '');
        const m = req.method();
        if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
        if (p.endsWith('/me/access')) return json(route, { permissions: ['conversation.claim', 'topic.read', 'topic.manage', 'ticket.read'], role_key: 'tenant_agent' });
        if (m === 'GET' && p === '/inbox/conversations') {
          state.listStatusParams.push(url.searchParams.get('status'));
          const wanted = url.searchParams.get('status') === 'closed' ? 'closed' : 'open';
          return json(route, { items: state.status === wanted ? [conversation()] : [], has_more: false });
        }
        if (m === 'GET' && p === `/inbox/conversations/${CONV}`) return json(route, conversation());
        if (m === 'GET' && p === `/inbox/conversations/${CONV}/messages`) return json(route, { items: [{ id: 'm1', conversation_id: CONV, direction: 'inbound', message_type: 'text', body: 'preciso de ajuda', status: 'received', created_at: now }], has_more: false });
        if (m === 'GET' && p === `/inbox/conversations/${CONV}/attendance-context`) {
          return json(route, {
            contact_id: 'ct-1',
            attendances: [{ id: 'cl-1', conversation_id: 'c-0', closed_by_user_id: 'u1', source: 'agent', reason: 'resolved', note: '', summary: 'Link voltou depois de reiniciar a ONU.', summary_truth: 'agent_confirmed', local_tickets_closed: 1, tickets_kept: 0, created_at: '2026-10-01T12:00:00Z', follow_ups: [] }],
            open_follow_ups: state.followUps.filter((f) => f.status === 'open'),
          });
        }
        if (m === 'POST' && /^\/follow-ups\/[^/]+\/resolve$/.test(p)) {
          const id = p.split('/')[2];
          const f = state.followUps.find((x) => x.id === id)!;
          f.status = 'done';
          state.posts.push({ path: p, body: JSON.parse(req.postData() ?? '{}') });
          return json(route, f);
        }
        if (m === 'POST' && p === `/inbox/conversations/${CONV}/finalize`) {
          state.posts.push({ path: p, body: JSON.parse(req.postData() ?? '{}') });
          state.status = 'closed';
          return json(route, { changed: true, closure: { id: 'cl-2', conversation_id: CONV, reason: 'resolved', follow_ups: [] } });
        }
        if (p.endsWith('/ticket')) return json(route, { local_ticket_id: 'lt-1', linked: false });
        if (p.endsWith('/crm/companies')) return json(route, { items: [] });
        if (p.endsWith('/topics') || p.endsWith('/ambiguities')) return json(route, { items: [] });
        return json(route, { error: 'not mocked' }, 404);
      });
    },
  };
}

test.describe('Finalizar atendimento (ADR-0020)', () => {
  test('ver pendências do contato, concluir uma, finalizar e ver a conversa encerrada', async ({ page }) => {
    const api = makeApi(page);
    await api.install();
    await page.goto('/inbox');
    await page.getByText('Maria Souza').first().click();

    // o que aconteceu antes com este contato
    const memory = page.getByRole('region', { name: 'Histórico do contato' });
    await expect(memory).toBeVisible({ timeout: 15_000 });
    await expect(memory.getByText('Ligar com o resultado da visita técnica')).toBeVisible();
    await expect(memory.getByText(/Atrasada/)).toBeVisible();
    await expect(memory.getByText(/Link voltou depois de reiniciar a ONU/)).toBeVisible();

    // concluir uma pendência
    await memory.getByRole('button', { name: 'Concluir: Ligar com o resultado da visita técnica' }).click();
    await expect(memory.getByText('Ligar com o resultado da visita técnica')).toHaveCount(0);
    expect(api.state.posts[0]).toMatchObject({ path: '/follow-ups/f-1/resolve', body: { status: 'done' } });

    // finalizar
    await page.getByRole('button', { name: /Finalizar atendimento/ }).click();
    const dialog = page.getByRole('dialog', { name: 'Finalizar atendimento' });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel('Motivo').selectOption('resolved');
    await dialog.getByLabel('Resumo do atendimento').fill('Reiniciamos a ONU e a conexão voltou.');
    await dialog.getByRole('button', { name: 'Adicionar item' }).click();
    await dialog.getByLabel('Tipo do item 1').selectOption('promise');
    await dialog.getByLabel('Texto do item 1').fill('Ligar na sexta para confirmar a estabilidade');
    await page.screenshot({ path: 'test-results/attendance-ui/01-dialogo.png' });
    await dialog.getByRole('button', { name: 'Finalizar atendimento' }).click();
    await expect(page.getByRole('dialog')).toHaveCount(0);

    const fin = api.state.posts.find((x) => x.path.endsWith('/finalize'))!;
    expect(fin.body).toMatchObject({ reason: 'resolved', summary: 'Reiniciamos a ONU e a conexão voltou.', follow_ups: [{ kind: 'promise', text: 'Ligar na sexta para confirmar a estabilidade' }] });

    // a conversa passa a finalizada: sem composer, sem ações
    await expect(page.getByText('Atendimento finalizado.', { exact: true })).toBeVisible();
    await expect(page.getByText(/Se o contato escrever de novo, abre-se um atendimento novo/)).toBeVisible();
    await expect(page.getByPlaceholder('Escreva uma resposta...')).toHaveCount(0);
    await expect(page.getByRole('button', { name: /Finalizar atendimento/ })).toHaveCount(0);
    await page.screenshot({ path: 'test-results/attendance-ui/02-finalizada.png' });
  });

  test('a lista padrão é a fila de trabalho; "Encerradas" pede as finalizadas', async ({ page }) => {
    const api = makeApi(page);
    await api.install();
    api.state.status = 'closed';
    await page.goto('/inbox');
    await expect(page.getByText('Maria Souza')).toHaveCount(0); // finalizada: fora da lista padrão
    await page.getByRole('tab', { name: 'Encerradas' }).click();
    await expect(page.getByText('Maria Souza').first()).toBeVisible({ timeout: 15_000 });
    expect(api.state.listStatusParams).toContain('closed');
    expect(api.state.listStatusParams).toContain(null); // a primeira carga não pediu status: o servidor devolve só as abertas
  });
});
