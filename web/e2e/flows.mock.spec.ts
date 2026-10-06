import { test, expect, Page, Route } from '@playwright/test';

// O editor visual de fluxos (ADR-0019) em um navegador real contra uma API MOCKADA: renderização do canvas, ligação de portas,
// arrastar com o mouse, salvar com a revisão certa, validação ao vivo bloqueando o botão Publicar e o simulador.
// O backend equivalente é coberto pelos testes de integração em Go; aqui só vale o que um navegador pode quebrar.

const TENANT = '11111111-1111-1111-1111-111111111111';
const FLOW = 'f0f0f0f0-0000-0000-0000-000000000001';
const SHOTS = 'test-results/flows-ui';

const NODE_TYPES = [
  { type: 'trigger', label: 'Start', category: 'flow', side_effect: 'none', waits: false, terminal: false, ports: ['next'] },
  { type: 'end', label: 'End', category: 'flow', side_effect: 'none', waits: false, terminal: true, ports: [] },
  { type: 'send_message', label: 'Send message', category: 'conversation', side_effect: 'external', waits: false, terminal: false, ports: ['next', 'window_closed', 'error'] },
  { type: 'choice', label: 'Menu', category: 'conversation', side_effect: 'external', waits: true, terminal: false, ports: ['timeout', 'other'] },
  { type: 'human_handoff', label: 'Handoff', category: 'action', side_effect: 'local', waits: false, terminal: true, ports: [] },
];

function makeApi(page: Page) {
  const state = {
    revision: 1,
    saved: null as any,
    saves: 0,
    publishes: 0,
    simulations: [] as any[],
    validateWith: [] as any[],
  };
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const flow = () => ({
    id: FLOW, slug: 'recepcao', name: 'Recepção', description: '', type: 'INBOUND', status: 'draft', draft_revision: state.revision, active_version: null,
    priority: 100, is_default: false, trigger_filter: {}, restart_policy: 'new_conversation_only', source_template_slug: null, source_template_version: null,
    created_at: '2026-10-05T10:00:00Z', updated_at: '2026-10-05T10:00:00Z',
    definition: state.saved ?? { schema_version: 1, nodes: [], edges: [], variables: [], settings: {}, metadata: {} },
  });
  return {
    state,
    install: async () => {
      await page.addInitScript(([t]) => {
        localStorage.setItem('token', 'mock-token');
        localStorage.setItem('tenantId', t);
        localStorage.setItem('sessionActive', 'true');
        localStorage.setItem('user', JSON.stringify({ id: 'u1', email: 'admin@example.test', name: 'Admin' }));
      }, [TENANT]);
      await page.route('**/api/v1/**', async (route) => {
        const req = route.request();
        const p = new URL(req.url()).pathname.replace('/api/v1/tenants/' + TENANT, '');
        const m = req.method();
        if (p.endsWith('/events') || p.includes('/presence')) return route.abort();
        if (p.endsWith('/me/access')) return json(route, { role_key: 'tenant_admin', permissions: ['flow.view', 'flow.create', 'flow.edit', 'flow.test', 'flow.publish', 'flow.archive'] });
        if (m === 'GET' && p === '/flow-node-types') return json(route, { items: NODE_TYPES });
        if (m === 'GET' && p === '/queues') return json(route, { items: [{ id: 'q-1', name: 'Suporte', mode: 'manual', is_default: true, member_count: 1, available_count: 1, open_conversation_count: 0, created_at: '', updated_at: '' }] });
        if (m === 'GET' && p === '/flows') return json(route, { items: [] });
        if (m === 'GET' && p === `/flows/${FLOW}`) return json(route, flow());
        if (m === 'GET' && p === `/flows/${FLOW}/versions`) return json(route, { items: [] });
        if (m === 'POST' && p === '/flows/validate') {
          const def = JSON.parse(req.postData() ?? '{}').definition;
          const issues = state.validateWith.length ? state.validateWith : (def.nodes.some((n: any) => n.type === 'trigger') ? [] : [{ severity: 'error', code: 'missing_trigger', message: 'O fluxo não tem nó de início.' }]);
          return json(route, { valid: !issues.some((i: any) => i.severity === 'error'), issues });
        }
        if (m === 'PUT' && p === `/flows/${FLOW}/draft`) {
          const body = JSON.parse(req.postData() ?? '{}');
          if (body.revision !== state.revision) return json(route, { error: 'revision_conflict' }, 409);
          state.saved = body.definition;
          state.revision += 1;
          state.saves += 1;
          return json(route, { flow: flow(), issues: [] });
        }
        if (m === 'POST' && p === `/flows/${FLOW}/simulate`) {
          state.simulations.push(JSON.parse(req.postData() ?? '{}'));
          return json(route, { status: 'waiting_human', steps: [{ seq: 1, node_id: 'trigger', node_type: 'trigger', status: 'completed', port: 'next' }], messages: [{ step: 2, text: 'Olá! Como podemos ajudar?' }], effects: [{ step: 3, kind: 'handoff', detail: {} }], variables: {}, events_consumed: 1 });
        }
        if (m === 'POST' && p === `/flows/${FLOW}/publish`) {
          state.publishes += 1;
          return json(route, { version: { id: 'v1', version: 1, note: '', published_at: '2026-10-06T00:00:00Z', definition_hash: 'h', subflow_pins: {}, published_by: null }, warnings: [] }, 201);
        }
        return json(route, { error: 'not mocked: ' + m + ' ' + p }, 404);
      });
    },
  };
}

const node = (page: Page, label: RegExp) => page.getByRole('group', { name: label });

test.describe('Editor visual de fluxos (ADR-0019)', () => {
  test('montar, ligar, arrastar, validar, salvar e simular', async ({ page }) => {
    const api = makeApi(page);
    await api.install();
    await page.goto(`/flows/${FLOW}`);
    await expect(page.getByLabel('Nome do fluxo')).toHaveValue('Recepção', { timeout: 15_000 });
    await page.screenshot({ path: `${SHOTS}/01-vazio.png` });

    // sem nó de início o servidor reporta erro e a publicação fica bloqueada
    await page.getByRole('tab', { name: /Problemas/ }).click();
    await expect(page.getByText('O fluxo não tem nó de início.')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('button', { name: 'Publicar' })).toBeDisabled();

    // monta Início -> Mensagem -> Menu -> (Encerrar | Transferir)
    for (const l of ['Início', 'Enviar mensagem', 'Menu de opções', 'Encerrar', 'Transferir para humano']) {
      await page.getByRole('navigation', { name: 'Paleta de nós' }).getByRole('button', { name: new RegExp(`^${l}`) }).click();
    }
    await expect(node(page, /Nó Início/)).toBeVisible();
    await expect(node(page, /Nó Menu de opções/)).toBeVisible();

    // arrastar com o mouse real: o nó Encerrar vai para outra posição e a posição é gravada na grade
    const end = node(page, /Nó Encerrar/);
    await end.scrollIntoViewIfNeeded();
    const before = await end.boundingBox();
    const header = end.locator('div').first();
    const hb = (await header.boundingBox())!;
    await page.mouse.move(hb.x + 40, hb.y + 12);
    await page.mouse.down();
    await page.mouse.move(hb.x + 240, hb.y + 160, { steps: 8 });
    await page.mouse.up();
    const after = await end.boundingBox();
    expect(after!.x).toBeGreaterThan(before!.x + 100);
    expect(after!.y).toBeGreaterThan(before!.y + 100);

    // ligações pelo mouse: saída -> entrada
    const link = async (fromNode: RegExp, port: RegExp, toNode: RegExp) => {
      await node(page, fromNode).getByRole('button', { name: port }).click();
      await node(page, toNode).getByRole('button', { name: /^Entrada de/ }).click();
    };
    await link(/Nó Início/, /Saída segue/, /Nó Enviar mensagem/);
    await link(/Nó Enviar mensagem/, /Saída segue/, /Nó Menu de opções/);
    await link(/Nó Menu de opções/, /Saída Opção 1/, /Nó Encerrar/);
    await link(/Nó Menu de opções/, /Saída Opção 2/, /Nó Transferir para humano/);
    await link(/Nó Menu de opções/, /Saída sem resposta/, /Nó Transferir para humano/);
    await expect(page.getByRole('button', { name: /Remover ligação/ })).toHaveCount(5);

    // um ciclo é recusado com o motivo
    await node(page, /Nó Menu de opções/).getByRole('button', { name: /Saída outra resposta/ }).click();
    await expect(node(page, /Nó Enviar mensagem/).getByRole('button', { name: /^Entrada de/ })).toBeEnabled();
    await node(page, /Nó Enviar mensagem/).getByRole('button', { name: /^Entrada de/ }).click();
    await expect(page.getByRole('alert')).toContainText('ciclo');
    await page.getByRole('button', { name: 'Dispensar aviso' }).click();
    await expect(page.getByRole('button', { name: /Remover ligação/ })).toHaveCount(5);
    await page.screenshot({ path: `${SHOTS}/02-grafo.png` });

    // editar uma propriedade
    await node(page, /Nó Enviar mensagem/).getByText('Enviar mensagem').click();
    await page.getByLabel('Texto da mensagem').fill('Olá {{contact.name}}!');

    // salvar com a revisão certa; depois o Publicar liga (a validação ao vivo agora tem um início)
    await expect(page.getByText('Alterações não salvas')).toBeVisible();
    await page.getByRole('button', { name: 'Salvar rascunho' }).click();
    await expect(page.getByText('Rascunho salvo.')).toBeVisible();
    expect(api.state.saves).toBe(1);
    const saved = api.state.saved;
    expect(saved.nodes.map((n: any) => n.type).sort()).toEqual(['choice', 'end', 'human_handoff', 'send_message', 'trigger']);
    expect(saved.edges).toHaveLength(5);
    expect(saved.nodes.find((n: any) => n.type === 'send_message').config.text).toBe('Olá {{contact.name}}!');
    expect(saved.nodes.find((n: any) => n.type === 'end').position.x % 20).toBe(0); // na grade
    await expect(page.getByRole('button', { name: 'Publicar' })).toBeEnabled({ timeout: 10_000 });

    // simulador: roda o cenário no que está no editor, mostra o caminho e acende os nós percorridos
    await page.getByRole('tab', { name: 'Simular' }).click();
    await page.getByRole('button', { name: 'Simular' }).click();
    await expect(page.getByTestId('simulation-result')).toContainText('Olá! Como podemos ajudar?');
    await expect(page.getByTestId('simulation-result')).toContainText('Transferiria para um humano');
    expect(api.state.simulations).toHaveLength(1);
    await page.screenshot({ path: `${SHOTS}/03-simulacao.png` });

    // publicar
    await page.getByRole('button', { name: 'Publicar' }).click();
    await page.getByLabel(/Nota da versão/).fill('primeira versão');
    await page.getByRole('dialog').getByRole('button', { name: 'Publicar' }).click();
    await expect(page.getByText(/Versão 1 publicada/)).toBeVisible();
    expect(api.state.publishes).toBe(1);
  });

  test('conflito de edição: outra pessoa salvou antes', async ({ page }) => {
    const api = makeApi(page);
    await api.install();
    await page.goto(`/flows/${FLOW}`);
    await expect(page.getByLabel('Nome do fluxo')).toHaveValue('Recepção', { timeout: 15_000 });
    await page.getByRole('navigation', { name: 'Paleta de nós' }).getByRole('button', { name: /^Início/ }).click();
    api.state.revision = 7; // alguém salvou nesse meio tempo
    await page.getByRole('button', { name: 'Salvar rascunho' }).click();
    await expect(page.getByRole('alert')).toContainText('alterado por outra pessoa');
    await expect(page.getByRole('button', { name: 'Salvar rascunho' })).toBeDisabled();
    await expect(page.getByRole('button', { name: 'Publicar' })).toBeDisabled();
    await page.screenshot({ path: `${SHOTS}/04-conflito.png` });
  });
  test('botão voltar/avançar do navegador respeita alterações não salvas (router de dados)', async ({ page }) => {
    const api = makeApi(page);
    await api.install();
    await page.goto(`/flows/${FLOW}`);
    await expect(page.getByLabel('Nome do fluxo')).toHaveValue('Recepção', { timeout: 15_000 });
    // dá ao histórico uma entrada à frente do editor: vai para outra página do app e volta com o botão voltar
    await page.getByRole('link', { name: /Contatos/ }).first().click();
    await expect(page).toHaveURL(/\/contacts/);
    await page.goBack();
    await expect(page.getByLabel('Nome do fluxo')).toHaveValue('Recepção');
    // sem alterações: avançar é livre
    await page.goForward();
    await expect(page).toHaveURL(/\/contacts/);
    await page.goBack();
    await expect(page.getByLabel('Nome do fluxo')).toHaveValue('Recepção');
    // com alteração não salva: avançar pergunta; recusar mantém o editor e o rascunho
    await page.getByRole('navigation', { name: 'Paleta de nós' }).getByRole('button', { name: /^Início/ }).click();
    await expect(page.getByText('Alterações não salvas')).toBeVisible();
    page.once('dialog', (d) => { expect(d.message()).toContain('alterações não salvas'); void d.dismiss(); });
    await page.goForward();
    await expect(page).toHaveURL(new RegExp(`/flows/${FLOW}$`));
    await expect(page.getByRole('group', { name: /Nó Início/ })).toBeVisible();
    // aceitar sai da página
    page.once('dialog', (d) => { void d.accept(); });
    await page.goForward();
    await expect(page).toHaveURL(/\/contacts/);
  });
});
