import { test, expect } from '@playwright/test';
import { api, env, hubctl, signIn, sql } from './support';

// ADR-0038 phase 5: the Hub in a real browser, real API, real RLS. Serial: each scenario builds on the data the previous one left.
test.describe.configure({ mode: 'serial' });

test('Conversas junta as instâncias liberadas pelo Hub, com o logo de origem, e filtra por instância', async ({ page }) => {
  await signIn(page, 'agent1@e2e.test');
  await page.goto('/inbox');
  await expect(page.getByRole('heading', { name: 'Conversas' })).toBeVisible();
  const list = page.getByRole('list', { name: 'Conversas do Hub' });
  await expect(list.getByText('Jose Carlos')).toBeVisible(); // instância A
  await expect(list.getByText('Maria Souza')).toBeVisible(); // instância B
  await expect(list.getByText('Ana Lima')).toHaveCount(0); // C: o agente não tem acesso
  await expect(list.getByRole('img', { name: 'WhatsApp' }).first()).toBeVisible();
  await expect(page.getByRole('link', { name: 'Hub', exact: true })).toHaveCount(0); // não existe item "Hub" no menu

  await page.getByRole('button', { name: 'Filtrar por instância' }).click();
  await page.getByLabel('NorteNet').check();
  await expect(list.getByText('Jose Carlos')).toHaveCount(0);
  await expect(list.getByText('Maria Souza')).toBeVisible();

  // abas por instância (ADR-0040 §7): Todas + as instâncias liberadas; a C, sem acesso, não existe na barra
  const bar = page.getByRole('tablist', { name: 'Instâncias' });
  await expect(bar.getByRole('tab', { name: 'Todas' })).toBeVisible();
  await expect(bar.getByRole('tab', { name: 'NorteNet' })).toBeVisible();
  await expect(bar.getByRole('tab')).toHaveCount(3);
  await bar.getByRole('tab', { name: 'NorteNet' }).click();
  await expect(page.getByRole('note').first()).toContainText('chegam na próxima etapa');
  await expect(list.getByText('Maria Souza')).toBeVisible();
  await expect(list.getByText('Jose Carlos')).toHaveCount(0);
});

test('assumir e responder pelo Hub grava a mensagem na instância certa', async ({ page }) => {
  await signIn(page, 'agent1@e2e.test');
  await page.goto('/inbox');
  await page.getByRole('list', { name: 'Conversas do Hub' }).getByText('Jose Carlos').click();
  await expect(page.getByText('Assuma a conversa para responder como ISP Roraima.')).toBeVisible();
  await page.getByRole('button', { name: 'Assumir' }).click();
  await expect(page.getByText('Respondendo como ISP Roraima')).toBeVisible();
  await page.getByRole('textbox').fill('Olá José, já estamos verificando');
  await page.getByRole('button', { name: 'Enviar mensagem' }).click();
  await expect(page.getByText('Olá José, já estamos verificando')).toBeVisible();
  expect(sql(`SELECT count(*) FROM messages m JOIN conversations c ON c.id = m.conversation_id WHERE m.direction = 'outbound' AND c.tenant_id = '${env.ta}' AND m.body = 'Olá José, já estamos verificando'`)).toBe('1');
  expect(sql(`SELECT count(*) FROM messages WHERE direction = 'outbound' AND tenant_id <> '${env.ta}'`)).toBe('0');
});

test('quem só lê vê a conversa mas não pode assumir nem responder', async ({ page }) => {
  await signIn(page, 'reader@e2e.test');
  await page.goto('/inbox');
  await page.getByText('Jose Carlos').click();
  await expect(page.getByRole('note')).toContainText('Somente leitura');
  await expect(page.getByRole('button', { name: 'Assumir' })).toHaveCount(0);
  await expect(page.getByRole('textbox')).toHaveCount(0);
  // e a API também recusa (não é só a tela)
  const open = await api(page, `/hubs/${env.hub}/inbox`);
  expect(open.status).toBe(200);
});

test('transferir: quem está com a conversa escolhe só entre quem pode responder aquela instância', async ({ page, browser }) => {
  await signIn(page, 'agent1@e2e.test');
  await page.goto('/inbox');
  await page.getByRole('list', { name: 'Conversas do Hub' }).getByText('Jose Carlos').click();
  await page.getByRole('button', { name: 'Transferir conversa' }).click();
  const dialog = page.getByRole('dialog', { name: 'Transferir conversa' });
  await expect(dialog.getByLabel('Carla Agente')).toBeVisible(); // responde A
  await expect(dialog.getByLabel('Davi Leitor')).toHaveCount(0); // só lê A
  await expect(dialog.getByLabel('Aline Admin')).toHaveCount(0); // administra o Hub mas não tem grant em A
  await dialog.getByLabel('Carla Agente').check();
  await dialog.getByRole('button', { name: 'Transferir' }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText('Esta conversa está com outro operador.')).toBeVisible();

  expect(sql(`SELECT count(*) FROM assignment_events WHERE reason = 'hub_transfer' AND tenant_id = '${env.ta}'`)).toBe('1');
  expect(sql(`SELECT count(*) FROM audit_events WHERE action = 'hub.conversation.transferred' AND tenant_id = '${env.ta}'`)).toBe('1');

  // a Carla agora tem a conversa: responde e pode transferir
  const other = await browser.newContext();
  const carla = await other.newPage();
  await signIn(carla, 'agent2@e2e.test');
  await carla.goto('/inbox');
  await carla.getByRole('list', { name: 'Conversas do Hub' }).getByText('Jose Carlos').click();
  await expect(carla.getByText('Respondendo como ISP Roraima')).toBeVisible();
  await expect(carla.getByRole('button', { name: 'Transferir conversa' })).toBeVisible();
  // o Bruno não pode mais transferir (a API recusa: não é só a tela)
  const resp = await page.request.post(`/api/v1/hubs/${env.hub}/inbox/${sql(`SELECT id FROM hub_inbox_items WHERE tenant_id = '${env.ta}' LIMIT 1`)}/transfer`, { data: { expected_tenant_id: env.ta, to_user_id: null } });
  expect(resp.status()).toBe(409);
  await other.close();
});

test('delegação: o operador libera a gestão, o administrador marca quem gerencia e o agente cria a conexão pelo Hub', async ({ page, browser }) => {
  // 1) o operador (administrador do Hub) delega "integrações" na instância A
  await signIn(page, 'admin@e2e.test');
  await page.goto('/acessos?aba=instancias');
  const card = page.getByRole('region', { name: 'Instância ISP Roraima' });
  await card.getByLabel(/Integrações de retaguarda/).click();
  await expect(card.getByLabel(/Integrações de retaguarda/)).toBeChecked();
  expect(sql(`SELECT management_scopes::text FROM hub_tenant_service_contracts WHERE tenant_id = '${env.ta}'`)).toBe('{integrations}');
  // sem a chave "Gerenciar" ninguém além do administrador gerencia
  const carlaId = sql(`SELECT id FROM users WHERE email = 'agent2@e2e.test'`);
  const carla0 = await browser.newContext();
  const c0 = await carla0.newPage();
  await signIn(c0, 'agent2@e2e.test');
  await c0.goto('/inbox');
  expect((await api(c0, `/hubs/${env.hub}/instances/${env.ta}/channels/connections`)).status).toBe(404);
  await expect(c0.getByRole('link', { name: 'Canais das instâncias' })).toHaveCount(0);
  await carla0.close();

  // 2) o administrador marca a Carla como gerente da instância A
  await page.getByRole('tab', { name: 'Agentes e permissões' }).click();
  const box = page.getByLabel('Gerenciar ISP Roraima — agent2@e2e.test');
  await box.click();
  await expect(box).toBeChecked();
  expect(sql(`SELECT can_manage FROM effective_access_grants WHERE user_id = '${carlaId}' AND tenant_id = '${env.ta}'`)).toBe('t');
  await expect(page.getByLabel('Gerenciar ISP Roraima — agent1@e2e.test')).not.toBeChecked();

  // 3) a Carla abre "Canais das instâncias" e conecta o CRM da instância A
  const ctx = await browser.newContext();
  const carla = await ctx.newPage();
  await signIn(carla, 'agent2@e2e.test');
  await carla.goto('/inbox');
  await carla.getByRole('link', { name: 'Canais das instâncias' }).first().click();
  await carla.getByRole('link', { name: 'Abrir canais e integrações' }).click();
  await expect(carla.getByRole('heading', { name: /Canais e integrações — ISP Roraima/ })).toBeVisible();
  await carla.getByRole('button', { name: 'Adicionar canal' }).first().click();
  await carla.getByRole('dialog', { name: 'Adicionar canal' }).getByText('CRM K3G').click();
  const form = carla.getByRole('dialog', { name: 'Conectar CRM K3G' });
  await form.getByLabel(/URL da API/).fill('https://crm.exemplo.test');
  await form.getByLabel(/Token de API/).fill('segredo-do-crm-12345');
  await form.getByRole('button', { name: 'Salvar conexão' }).click();
  await expect(carla.getByText('CRM K3G').first()).toBeVisible();

  // o que de fato aconteceu
  expect(sql(`SELECT count(*) FROM channel_connections WHERE tenant_id = '${env.ta}' AND channel = 'erp' AND provider = 'k3g_crm'`)).toBe('1');
  expect(sql(`SELECT count(*) FROM channel_credentials cr JOIN channel_connections c ON c.id = cr.connection_id WHERE c.tenant_id = '${env.ta}' AND c.channel = 'erp'`)).toBe('1');
  expect(sql(`SELECT count(*) FROM channel_connections WHERE channel = 'erp' AND tenant_id <> '${env.ta}'`)).toBe('0');
  expect(sql(`SELECT metadata->>'via' || '/' || (metadata->>'hub_id' = '${env.hub}')::text FROM audit_events WHERE action = 'channel.connection_created' AND actor_id = '${carlaId}' AND tenant_id = '${env.ta}'`)).toBe('hub/true');
  const list = await api(carla, `/hubs/${env.hub}/instances/${env.ta}/channels/connections`);
  expect(list.status).toBe(200);
  expect(list.body).not.toContain('segredo-do-crm-12345');
  expect(sql(`SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%segredo-do-crm-12345%'`)).toBe('0');
  // o escopo "canais" (WhatsApp) NÃO foi delegado: a mesma pessoa recebe 403 ao tentar criar uma linha
  const wa = await carla.request.post(`/api/v1/hubs/${env.hub}/instances/${env.ta}/channels/connections`, { data: { provider: 'waha', risk_acknowledged: true } });
  expect([403, 503]).toContain(wa.status());
  expect(sql(`SELECT count(*) FROM channel_connections WHERE tenant_id = '${env.ta}' AND provider = 'waha' AND external_number_id NOT LIKE 'e2e-%'`)).toBe('0');
  // outra instância do mesmo Hub: nada
  expect((await api(carla, `/hubs/${env.hub}/instances/${env.tb}/channels/connections`)).status).toBe(404);
  // o administrador retira a gestão e ela perde o acesso na hora
  await box.click();
  await expect(box).not.toBeChecked();
  expect((await api(carla, `/hubs/${env.hub}/instances/${env.ta}/channels/connections`)).status).toBe(404);
  await ctx.close();
});

test('segurança no navegador real: quem não administra o Hub não vê painel nem equipes, e uma instância sem acesso não existe', async ({ page }) => {
  await signIn(page, 'agent1@e2e.test');
  await page.goto('/acessos');
  await expect(page.getByText('Painel de acessos indisponível')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Acessos' })).toHaveCount(0);
  expect((await api(page, `/hubs/${env.hub}/pools`)).status).toBe(404);
  expect((await api(page, `/hubs/${env.hub}/access`)).status).toBe(404);
  const create = await page.request.post(`/api/v1/hubs/${env.hub}/pools`, { data: { name: 'Invasora', distribution: 'round_robin' } });
  expect(create.status()).toBe(404);
  expect(sql(`SELECT count(*) FROM work_pools`)).toBe('0');
  // a instância C não é dele: nem a conversa, nem a gestão
  const itemC = sql(`SELECT id FROM hub_inbox_items WHERE tenant_id = '${env.tc}' LIMIT 1`);
  expect((await api(page, `/hubs/${env.hub}/inbox/${itemC}`)).status).toBe(404);
  expect((await api(page, `/hubs/${env.hub}/instances/${env.tc}/channels/connections`)).status).toBe(404);

  await page.context().clearCookies();
  await signIn(page, 'stranger@e2e.test'); // não está em Hub nenhum
  expect((await api(page, '/hubs')).body).toContain('"items":[]');
  expect((await api(page, `/hubs/${env.hub}/inbox`)).status).toBe(404);
});

test('equipes: o administrador monta a equipe e a distribuição automática entrega as conversas novas pelo rodízio', async ({ page }) => {
  await signIn(page, 'admin@e2e.test');
  await page.goto('/acessos?aba=equipes');
  await page.getByRole('textbox', { name: 'Nova equipe' }).fill('Atendimento A');
  await page.getByRole('button', { name: 'Criar equipe' }).click();
  const team = page.getByRole('article', { name: 'Equipe Atendimento A' });
  await team.getByLabel('agent1@e2e.test na equipe Atendimento A').check();
  await team.getByLabel('agent2@e2e.test na equipe Atendimento A').check();
  await team.getByRole('button', { name: 'Salvar integrantes' }).click();
  await expect(page.getByRole('alert')).toContainText('Integrantes salvos');
  await team.getByLabel('ISP Roraima atendida pela equipe Atendimento A').check();
  await team.getByRole('button', { name: 'Salvar instâncias' }).click();
  await expect(page.getByRole('alert')).toContainText('Instâncias salvas');
  await team.getByLabel('Distribuição da equipe Atendimento A').selectOption('round_robin');
  await expect(team.getByLabel('Distribuição da equipe Atendimento A')).toHaveValue('round_robin');
  expect(sql(`SELECT distribution || '/' || (SELECT count(*) FROM work_pool_members m WHERE m.work_pool_id = p.id) || '/' || (SELECT count(*) FROM work_pool_instances i WHERE i.work_pool_id = p.id) FROM work_pools p WHERE name = 'Atendimento A'`)).toBe('round_robin/2/1');

  // quatro conversas novas e sem dono na instância A
  const tag = Date.now();
  for (let i = 0; i < 4; i++) {
    sql(`WITH c AS (INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES (gen_random_uuid(), '${env.ta}', 'Novo ${tag}-${i}', '+55929${tag % 100000}${i}') RETURNING id),
              v AS (INSERT INTO conversations (id, tenant_id, contact_id) SELECT gen_random_uuid(), '${env.ta}', id FROM c RETURNING id)
         INSERT INTO messages (tenant_id, conversation_id, direction, body) SELECT '${env.ta}', id, 'inbound', 'Oi ${i}' FROM v`);
  }
  hubctl('reconcile');
  const out = hubctl('distribute');
  expect(out).toMatch(/distributed: \d+ assigned/);
  const per = sql(`SELECT u.email || '=' || count(*) FROM conversations c JOIN users u ON u.id = c.assigned_to_user_id WHERE c.tenant_id = '${env.ta}' AND c.id IN (SELECT conversation_id FROM assignment_events WHERE reason = 'hub_pool') GROUP BY u.email ORDER BY u.email`);
  expect(per).toBe('agent1@e2e.test=2\nagent2@e2e.test=2');
  // ninguém que só lê (ou não está na equipe) recebeu, e cada atribuição ficou no histórico como do sistema
  expect(sql(`SELECT count(*) FROM conversations c JOIN users u ON u.id = c.assigned_to_user_id WHERE u.email IN ('reader@e2e.test','admin@e2e.test','stranger@e2e.test')`)).toBe('0');
  expect(sql(`SELECT count(*) FROM assignment_events WHERE reason = 'hub_pool' AND actor_source = 'system' AND changed_by IS NULL`)).toBe('4');
  // rodar de novo não muda nada
  expect(hubctl('distribute')).toContain('0 assigned');
  // a pessoa vê a conversa que recebeu, já como dela (sem precisar assumir)
  const ctx2 = await page.context().browser()!.newContext();
  const bruno = await ctx2.newPage();
  await signIn(bruno, 'agent1@e2e.test');
  await bruno.goto('/inbox');
  const mine = sql(`SELECT ct.display_name FROM conversations c JOIN contacts ct ON ct.id = c.contact_id JOIN users u ON u.id = c.assigned_to_user_id WHERE u.email = 'agent1@e2e.test' AND ct.display_name LIKE 'Novo ${tag}-%' ORDER BY ct.display_name LIMIT 1`);
  expect(mine).toMatch(/^Novo /);
  await bruno.getByRole('list', { name: 'Conversas do Hub' }).getByText(mine).click();
  await expect(bruno.getByText('Respondendo como ISP Roraima')).toBeVisible();
  await ctx2.close();
});

test('auditoria: o administrador vê quem mudou o quê, e só ele', async ({ page }) => {
  await signIn(page, 'admin@e2e.test');
  await page.goto('/acessos?aba=auditoria');
  const table = page.getByRole('table');
  await expect(table.getByText('Gestão delegada ao Hub alterada').first()).toBeVisible();
  await expect(table.getByText('Conexão de canal/integração criada').first()).toBeVisible();
  await expect(table.getByText('pelo Hub').first()).toBeVisible(); // a conexão feita pela Carla através do Hub
  await expect(table.getByText('Equipe criada').first()).toBeVisible();
  await expect(table.getByText('Chave “Gerenciar” alterada').first()).toBeVisible();
  // mensagens e atendimentos não entram, e nenhum segredo aparece
  await expect(page.getByText('hub.message.sent')).toHaveCount(0);
  const body = await api(page, `/hubs/${env.hub}/audit?limit=200`);
  expect(body.status).toBe(200);
  expect(body.body).not.toContain('segredo-do-crm-12345');
  expect(body.body).not.toContain('hub.message');
  expect(body.body).not.toContain('hub.conversation');
  // filtrar por instância
  await page.getByLabel('Filtrar auditoria por instância').selectOption({ label: 'NorteNet' });
  await expect(table.getByText('NorteNet').first()).toBeVisible(); // as concessões iniciais de acesso à NorteNet
  await expect(table.getByText('ISP Roraima')).toHaveCount(0); // e nenhuma da outra instância

  const other = await page.context().browser()!.newContext();
  const p2 = await other.newPage();
  await signIn(p2, 'agent1@e2e.test');
  expect((await api(p2, `/hubs/${env.hub}/audit`)).status).toBe(404);
  await other.close();
});
