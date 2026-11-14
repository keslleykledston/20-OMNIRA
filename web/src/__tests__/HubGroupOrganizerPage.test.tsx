import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import HubGroupOrganizerPage from '../pages/HubGroupOrganizerPage';
import { renderAt, setSession } from './testUtils';

// Organizador de grupos (ADR-0059, Gate 3): lista/pastas/kanban, sem drag-and-drop (D-12) — mover um grupo é sempre
// um clique explícito num menu/botão que chama a MESMA rota HTTP do Gate 2. O backend é provado contra PostgreSQL
// real (grouporganizer_http_integration_test.go); aqui se confere só a tela.

vi.mock('axios');
const unauthorized = vi.hoisted(() => vi.fn());
vi.mock('../lib/session', async (orig) => ({ ...(await orig<typeof import('../lib/session')>()), handleUnauthorized: unauthorized }));

const HUB = 'hub-1';
const GROUP_UNCLASSIFIED = 'group-unclassified';
const GROUP_ALPHA = 'group-alpha';
const GROUP_SHARED = 'group-shared';

let hubs: object[];
let managed: object[];
let catalog: object[];
let relationships: object[];
let linksByGroup: Record<string, object[]>;
let discovered: object[];
let discoveredConnections: object[];
let classification: object[];

function entry(id: string, name: string) {
  return { id, hub_id: HUB, source_tenant_id: 'A', source_channel_connection_id: 'conn-1', provider_group_id: '120300000000@g.us', display_name: name, source_state: 'available', version: 1, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', last_seen_at: '2026-01-01T00:00:00Z' };
}

function link(id: string, groupId: string, tenantId: string, role = 'consumer', version = 1) {
  return { id, hub_id: HUB, hub_group_id: groupId, tenant_id: tenantId, role, status: 'active', assigned_at: '2026-01-01T00:00:00Z', version, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' };
}

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    const u = String(url);
    if (u.endsWith('/hubs')) return { data: { items: hubs } };
    if (u.endsWith('/managed')) return { data: { items: managed } };
    if (u.endsWith('/groups/catalog')) return { data: { items: catalog } };
    if (u.endsWith('/groups/relationships')) return { data: { items: relationships } };
    if (u.endsWith('/groups/discover-connections')) return { data: { items: discoveredConnections } };
    if (u.endsWith('/groups/discover')) return { data: { items: discovered } };
    if (u.endsWith('/groups/links')) return { data: { items: Object.values(linksByGroup).flat() } };
    if (u.endsWith('/groups/classification')) return { data: { items: classification } };
    const linksMatch = /\/groups\/catalog\/([^/]+)\/links$/.exec(u);
    if (linksMatch) return { data: { items: linksByGroup[linksMatch[1]] ?? [] } };
    return Promise.reject({ response: { status: 404 } });
  });
  vi.mocked(axios.post).mockResolvedValue({ data: {} });
}

beforeEach(() => {
  vi.resetAllMocks();
  unauthorized.mockReset();
  localStorage.clear();
  setSession();
  hubs = [{ id: HUB, name: 'K3G Solutions', role: 'hub_agent', can_manage_groups: true }];
  managed = [{ tenant_id: 'A', name: 'ISP Roraima', scopes: ['groups'] }, { tenant_id: 'B', name: 'NorteNet', scopes: ['groups'] }];
  catalog = [entry(GROUP_UNCLASSIFIED, 'Suporte geral'), entry(GROUP_ALPHA, 'Clientes Alpha'), entry(GROUP_SHARED, 'Trânsito compartilhado')];
  relationships = [{ id: 'rel-1', hub_id: HUB, consumer_tenant_id: 'A', supplier_tenant_id: 'B', service_kind: 'trânsito', service_reference: '', status: 'active', valid_from: '2026-01-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }];
  linksByGroup = {
    [GROUP_UNCLASSIFIED]: [],
    [GROUP_ALPHA]: [link('l1', GROUP_ALPHA, 'A')],
    [GROUP_SHARED]: [link('l2', GROUP_SHARED, 'A'), link('l3', GROUP_SHARED, 'B', 'supplier')],
  };
  discovered = [];
  discoveredConnections = [];
  classification = [];
  serve();
});

describe('HubGroupOrganizerPage', () => {
  it('nothing delegated: shows the empty state and asks the API for nothing beyond /hubs', async () => {
    hubs = [{ id: HUB, name: 'K3G Solutions', role: 'hub_agent', can_manage_groups: false }];
    renderAt(<HubGroupOrganizerPage />);
    expect(await screen.findByText('Nenhum Hub para organizar grupos')).toBeInTheDocument();
    expect(vi.mocked(axios.get).mock.calls.some((c) => String(c[0]).endsWith('/groups/catalog'))).toBe(false);
  });

  it('lists every catalog entry with its origin, state and active links; unlinked ones say "Sem classificação"', async () => {
    renderAt(<HubGroupOrganizerPage />);
    const unclassifiedRow = (await screen.findByText('Suporte geral')).closest('tr')!;
    expect(within(unclassifiedRow).getByText('Sem classificação')).toBeInTheDocument();
    const alphaRow = screen.getByText('Clientes Alpha').closest('tr')!;
    expect(within(alphaRow).getAllByText('ISP Roraima').length).toBeGreaterThan(0); // origem E vínculo, ambos ISP Roraima aqui
    expect(within(alphaRow).getByText('Consumidor')).toBeInTheDocument();
  });

  it('busca os vínculos de todos os grupos numa única chamada, nunca uma por item do catálogo (achado ao vivo: o catálogo pode crescer sozinho por autodescoberta)', async () => {
    renderAt(<HubGroupOrganizerPage />);
    await screen.findByText('Suporte geral')
    const calls = vi.mocked(axios.get).mock.calls.map((c) => String(c[0]))
    expect(calls.filter((u) => u.endsWith('/groups/links')).length).toBe(1)
    expect(calls.some((u) => /\/groups\/catalog\/[^/]+\/links$/.test(u))).toBe(false)
  });

  it('mostra a classificação do CRM (purpose + conta) lida do tenant, só leitura, e "Sem classificação no CRM" quando o grupo ainda não tem uma', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, purpose: 'client_support', account_display_name: 'InfoRR Consultoria' }];
    renderAt(<HubGroupOrganizerPage />);
    const alphaRow = (await screen.findByText('Clientes Alpha')).closest('tr')!;
    expect(within(alphaRow).getByText('Cliente · Atendimento')).toBeInTheDocument();
    expect(within(alphaRow).getByText('InfoRR Consultoria')).toBeInTheDocument();
    const unclassifiedRow = screen.getByText('Suporte geral').closest('tr')!;
    expect(within(unclassifiedRow).getByText('Sem classificação no CRM')).toBeInTheDocument();
    const calls = vi.mocked(axios.get).mock.calls.map((c) => String(c[0]));
    expect(calls.filter((u) => u.endsWith('/groups/classification')).length).toBe(1);
  });

  it('pastas: agrupa por classificação (Cliente/Parceiro/Fornecedor/Interno), nunca por Tenant', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, purpose: 'client_support', account_display_name: 'InfoRR Consultoria' }];
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: /^Pastas/ }));
    const folder = screen.getByLabelText('Pasta Cliente · Atendimento');
    expect(within(folder).getByText('Clientes Alpha')).toBeInTheDocument();
    expect(within(folder).queryByText('Suporte geral')).toBeNull();
    expect(screen.queryByLabelText('Pasta ISP Roraima')).toBeNull();
  });

  it('pastas: alternando "Agrupar por" para Cliente do CRM, agrupa por conta vinculada (wa_groups.account_id), nunca por nome sozinho', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, purpose: 'client_support', account_id: 'acc-1', account_display_name: 'InfoRR Consultoria' }];
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: /^Pastas/ }));
    await userEvent.click(screen.getByRole('radio', { name: 'Cliente do CRM' }));
    const accountFolder = await screen.findByLabelText('Pasta InfoRR Consultoria');
    expect(within(accountFolder).getByText('Clientes Alpha')).toBeInTheDocument();
    const unlinkedFolder = screen.getByLabelText('Pasta Sem conta vinculada no CRM');
    expect(within(unlinkedFolder).getByText('Suporte geral')).toBeInTheDocument();
    expect(within(unlinkedFolder).getByText('Trânsito compartilhado')).toBeInTheDocument();
  });

  it('vincular cliente do CRM: a ação de verdade chama o endpoint JÁ EXISTENTE de grupos do tenant certo, nunca um caminho novo pelo Hub', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, wa_group_id: 'wa-alpha', purpose: 'other' }];
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      const u = String(url);
      if (u.endsWith('/hubs')) return { data: { items: hubs } };
      if (u.endsWith('/managed')) return { data: { items: managed } };
      if (u.endsWith('/groups/catalog')) return { data: { items: catalog } };
      if (u.endsWith('/groups/relationships')) return { data: { items: relationships } };
      if (u.endsWith('/groups/discover-connections')) return { data: { items: discoveredConnections } };
      if (u.endsWith('/groups/discover')) return { data: { items: discovered } };
      if (u.endsWith('/groups/links')) return { data: { items: Object.values(linksByGroup).flat() } };
      if (u.endsWith('/groups/classification')) return { data: { items: classification } };
      if (u.endsWith('/tenants/A/crm/companies')) return { data: { items: [{ id: 'co-1', name: 'InfoRR Consultoria', cnpj: '123' }] } };
      if (u.endsWith('/tenants/A/accounts')) return { data: { items: [] } };
      const linksMatch = /\/groups\/catalog\/([^/]+)\/links$/.exec(u);
      if (linksMatch) return { data: { items: linksByGroup[linksMatch[1]] ?? [] } };
      return Promise.reject({ response: { status: 404 } });
    });
    vi.mocked(axios.put).mockResolvedValue({ data: {} });

    renderAt(<HubGroupOrganizerPage />);
    const alphaRow = (await screen.findByText('Clientes Alpha')).closest('tr')!;
    await userEvent.click(within(alphaRow).getByRole('button', { name: 'Vincular cliente' }));
    const dialog = await screen.findByRole('dialog', { name: 'Vincular ao Cliente do CRM' });
    await userEvent.click(await within(dialog).findByRole('button', { name: /InfoRR Consultoria/ }));
    await waitFor(() =>
      expect(axios.put).toHaveBeenCalledWith(expect.stringContaining('/tenants/A/groups/wa-alpha/account'), { directory_company_id: 'co-1' }, expect.anything()),
    );
  });

  it('mostra também o(s) cliente(s) adicional(is) do grupo na classificação, junto com o principal (ADR-0059 addendum 8)', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, purpose: 'partner', account_display_name: 'InfoRR Consultoria', additional_accounts: [{ id: 'acc-2', name: 'IP Fibra' }] }];
    renderAt(<HubGroupOrganizerPage />);
    const alphaRow = (await screen.findByText('Clientes Alpha')).closest('tr')!;
    expect(within(alphaRow).getByText('InfoRR Consultoria')).toBeInTheDocument();
    expect(within(alphaRow).getByText('+ IP Fibra')).toBeInTheDocument();
  });

  it('pastas: Cliente do CRM mostra um grupo "swap" nas pastas dos DOIS clientes (primário + adicionais), nunca só na primeira (ADR-0059 addendum 8)', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, purpose: 'partner', account_id: 'acc-1', account_display_name: 'InfoRR Consultoria', additional_accounts: [{ id: 'acc-2', name: 'IP Fibra' }] }];
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: /^Pastas/ }));
    await userEvent.click(screen.getByRole('radio', { name: 'Cliente do CRM' }));
    const infoRRFolder = await screen.findByLabelText('Pasta InfoRR Consultoria');
    expect(within(infoRRFolder).getByText('Clientes Alpha')).toBeInTheDocument();
    const ipFibraFolder = screen.getByLabelText('Pasta IP Fibra');
    expect(within(ipFibraFolder).getByText('Clientes Alpha')).toBeInTheDocument();
  });

  it('vincular cliente do CRM: "+ Adicionar outro cliente" chama POST .../accounts, nunca o PUT do vínculo principal', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, wa_group_id: 'wa-alpha', purpose: 'other', account_id: 'acc-1', account_display_name: 'InfoRR Consultoria', additional_accounts: [] }];
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      const u = String(url);
      if (u.endsWith('/hubs')) return { data: { items: hubs } };
      if (u.endsWith('/managed')) return { data: { items: managed } };
      if (u.endsWith('/groups/catalog')) return { data: { items: catalog } };
      if (u.endsWith('/groups/relationships')) return { data: { items: relationships } };
      if (u.endsWith('/groups/discover-connections')) return { data: { items: discoveredConnections } };
      if (u.endsWith('/groups/discover')) return { data: { items: discovered } };
      if (u.endsWith('/groups/links')) return { data: { items: Object.values(linksByGroup).flat() } };
      if (u.endsWith('/groups/classification')) return { data: { items: classification } };
      if (u.endsWith('/tenants/A/crm/companies')) return { data: { items: [{ id: 'co-2', name: 'IP Fibra', cnpj: '456' }] } };
      if (u.endsWith('/tenants/A/accounts')) return { data: { items: [] } };
      const linksMatch = /\/groups\/catalog\/([^/]+)\/links$/.exec(u);
      if (linksMatch) return { data: { items: linksByGroup[linksMatch[1]] ?? [] } };
      return Promise.reject({ response: { status: 404 } });
    });
    vi.mocked(axios.post).mockResolvedValue({ data: {} });

    renderAt(<HubGroupOrganizerPage />);
    const alphaRow = (await screen.findByText('Clientes Alpha')).closest('tr')!;
    await userEvent.click(within(alphaRow).getByRole('button', { name: 'Alterar cliente' }));
    const dialog = await screen.findByRole('dialog', { name: 'Vincular ao Cliente do CRM' });
    await userEvent.click(within(dialog).getByRole('button', { name: '+ Adicionar outro cliente' }));
    await userEvent.click(await within(dialog).findByRole('button', { name: /IP Fibra/ }));
    await waitFor(() =>
      expect(axios.post).toHaveBeenCalledWith(expect.stringContaining('/tenants/A/groups/wa-alpha/accounts'), { directory_company_id: 'co-2' }, expect.anything()),
    );
  });

  it('vincular cliente do CRM: "Remover" num cliente adicional chama DELETE .../accounts/{id}, nunca o vínculo principal', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, wa_group_id: 'wa-alpha', purpose: 'other', account_id: 'acc-1', account_display_name: 'InfoRR Consultoria', additional_accounts: [{ id: 'acc-2', name: 'IP Fibra' }] }];
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      const u = String(url);
      if (u.endsWith('/hubs')) return { data: { items: hubs } };
      if (u.endsWith('/managed')) return { data: { items: managed } };
      if (u.endsWith('/groups/catalog')) return { data: { items: catalog } };
      if (u.endsWith('/groups/relationships')) return { data: { items: relationships } };
      if (u.endsWith('/groups/discover-connections')) return { data: { items: discoveredConnections } };
      if (u.endsWith('/groups/discover')) return { data: { items: discovered } };
      if (u.endsWith('/groups/links')) return { data: { items: Object.values(linksByGroup).flat() } };
      if (u.endsWith('/groups/classification')) return { data: { items: classification } };
      if (u.endsWith('/tenants/A/crm/companies')) return { data: { items: [] } };
      if (u.endsWith('/tenants/A/accounts')) return { data: { items: [] } };
      const linksMatch = /\/groups\/catalog\/([^/]+)\/links$/.exec(u);
      if (linksMatch) return { data: { items: linksByGroup[linksMatch[1]] ?? [] } };
      return Promise.reject({ response: { status: 404 } });
    });
    vi.mocked(axios.delete).mockResolvedValue({ data: {} });

    renderAt(<HubGroupOrganizerPage />);
    const alphaRow = (await screen.findByText('Clientes Alpha')).closest('tr')!;
    await userEvent.click(within(alphaRow).getByRole('button', { name: 'Alterar cliente' }));
    const dialog = await screen.findByRole('dialog', { name: 'Vincular ao Cliente do CRM' });
    expect(within(dialog).getByText('IP Fibra')).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole('button', { name: 'Remover' }));
    await waitFor(() =>
      expect(axios.delete).toHaveBeenCalledWith(expect.stringContaining('/tenants/A/groups/wa-alpha/accounts/acc-2'), expect.anything()),
    );
  });

  it('vincular cliente do CRM: desabilitado quando o grupo ainda não tem wa_groups (nunca aberto na tela de Grupos do tenant)', async () => {
    classification = [];
    renderAt(<HubGroupOrganizerPage />);
    const row = (await screen.findByText('Clientes Alpha')).closest('tr')!;
    expect(within(row).getByRole('button', { name: 'Vincular cliente' })).toBeDisabled();
  });

  it('pastas: deriva a pasta compartilhada SÓ da relação comercial + vínculos ativos dos dois lados, nunca manual', async () => {
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: /^Pastas/ }));
    expect(await screen.findByRole('heading', { name: 'ISP Roraima ↔ NorteNet' })).toBeInTheDocument();
    const shared = screen.getByLabelText('Pasta ISP Roraima ↔ NorteNet');
    expect(within(shared).getByText('Trânsito compartilhado')).toBeInTheDocument();
    // "Clientes Alpha" só está vinculado a A, não aparece na compartilhada
    expect(within(shared).queryByText('Clientes Alpha')).toBeNull();
  });

  it('kanban agrupa por classificação (Cliente/Parceiro/Fornecedor/Interno), não por Tenant; cartão só é arrastável com wa_group_id conhecido', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, wa_group_id: 'wa-alpha', purpose: 'client_support', account_display_name: 'InfoRR Consultoria' }];
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Kanban' }));
    const board = await screen.findByRole('list', { name: 'Quadro de grupos por pasta' });
    const alphaCol = within(board).getByLabelText('Coluna Cliente · Atendimento');
    expect(within(alphaCol).getByText('Clientes Alpha')).toBeInTheDocument();
    const otherCol = within(board).getByLabelText('Coluna Outros');
    expect(within(otherCol).getByText('Suporte geral')).toBeInTheDocument();
    expect(within(board).queryByLabelText('Coluna ISP Roraima')).toBeNull();
    // Clientes Alpha tem wa_group_id (classificado) — arrastável; Suporte geral nunca foi aberto no tenant — não é.
    expect(within(alphaCol).getByText('Clientes Alpha').closest('[draggable]')).toHaveAttribute('draggable', 'true');
    expect(within(otherCol).getByText('Suporte geral').closest('[draggable]')).toHaveAttribute('draggable', 'false');
  });

  it('kanban: arrastar um cartão para outra coluna de Classificação chama o PATCH de purpose já existente, no tenant certo', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, wa_group_id: 'wa-alpha', purpose: 'other', additional_accounts: [] }];
    vi.mocked(axios.patch).mockResolvedValue({ data: {} });
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Kanban' }));
    const board = await screen.findByRole('list', { name: 'Quadro de grupos por pasta' });
    const card = within(board).getByText('Clientes Alpha').closest('[draggable]') as HTMLElement;
    const targetCol = within(board).getByLabelText('Coluna Cliente · Atendimento');
    const dataTransfer = { getData: vi.fn().mockReturnValue(GROUP_ALPHA), setData: vi.fn() };
    fireEvent.dragStart(card, { dataTransfer });
    fireEvent.dragOver(targetCol, { dataTransfer });
    fireEvent.drop(targetCol, { dataTransfer });
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.patch).mock.calls[0][0])).toBe('/api/v1/tenants/A/groups/wa-alpha');
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ purpose: 'client_support' });
  });

  it('kanban: soltar na MESMA coluna em que o cartão já está é um no-op, nunca uma chamada ao servidor', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, wa_group_id: 'wa-alpha', purpose: 'client_support', additional_accounts: [] }];
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Kanban' }));
    const board = await screen.findByRole('list', { name: 'Quadro de grupos por pasta' });
    const card = within(board).getByText('Clientes Alpha').closest('[draggable]') as HTMLElement;
    const sameCol = within(board).getByLabelText('Coluna Cliente · Atendimento');
    const dataTransfer = { getData: vi.fn().mockReturnValue(GROUP_ALPHA), setData: vi.fn() };
    fireEvent.dragStart(card, { dataTransfer });
    fireEvent.drop(sameCol, { dataTransfer });
    expect(axios.patch).not.toHaveBeenCalled();
  });

  it('kanban: nunca aceita um drop na pasta compartilhada (derivada de relação comercial + vínculos, D-07) — nenhuma chamada ao servidor', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, wa_group_id: 'wa-alpha', purpose: 'other', additional_accounts: [] }];
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Kanban' }));
    const board = await screen.findByRole('list', { name: 'Quadro de grupos por pasta' });
    const card = within(board).getByText('Clientes Alpha').closest('[draggable]') as HTMLElement;
    const sharedCol = within(board).getByLabelText('Coluna ISP Roraima ↔ NorteNet');
    const dataTransfer = { getData: vi.fn().mockReturnValue(GROUP_ALPHA), setData: vi.fn() };
    fireEvent.dragStart(card, { dataTransfer });
    fireEvent.drop(sharedCol, { dataTransfer });
    expect(axios.patch).not.toHaveBeenCalled();
    expect(axios.put).not.toHaveBeenCalled();
  });

  it('kanban: agrupando por Cliente do CRM, arrastar para a pasta de outro cliente chama o vínculo de conta já existente (PUT .../account)', async () => {
    classification = [
      { hub_group_id: GROUP_ALPHA, wa_group_id: 'wa-alpha', purpose: 'other', account_id: 'acc-1', account_display_name: 'InfoRR Consultoria', additional_accounts: [] },
      { hub_group_id: GROUP_SHARED, wa_group_id: 'wa-shared', purpose: 'other', account_id: 'acc-2', account_display_name: 'IP Fibra', additional_accounts: [] },
    ];
    vi.mocked(axios.put).mockResolvedValue({ data: {} });
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Kanban' }));
    await userEvent.click(screen.getByRole('radio', { name: 'Cliente do CRM' }));
    const board = await screen.findByRole('list', { name: 'Quadro de grupos por pasta' });
    const card = within(board).getByText('Clientes Alpha').closest('[draggable]') as HTMLElement;
    const targetCol = within(board).getByLabelText('Coluna IP Fibra');
    const dataTransfer = { getData: vi.fn().mockReturnValue(GROUP_ALPHA), setData: vi.fn() };
    fireEvent.dragStart(card, { dataTransfer });
    fireEvent.drop(targetCol, { dataTransfer });
    await waitFor(() =>
      expect(axios.put).toHaveBeenCalledWith(expect.stringContaining('/tenants/A/groups/wa-alpha/account'), { account_id: 'acc-2' }, expect.anything()),
    );
  });

  it('kanban: agrupando por Cliente do CRM, arrastar para "Sem conta vinculada" chama o desvínculo já existente (DELETE .../account)', async () => {
    classification = [{ hub_group_id: GROUP_ALPHA, wa_group_id: 'wa-alpha', purpose: 'other', account_id: 'acc-1', account_display_name: 'InfoRR Consultoria', additional_accounts: [] }];
    vi.mocked(axios.delete).mockResolvedValue({ data: {} });
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Kanban' }));
    await userEvent.click(screen.getByRole('radio', { name: 'Cliente do CRM' }));
    const board = await screen.findByRole('list', { name: 'Quadro de grupos por pasta' });
    const card = within(board).getByText('Clientes Alpha').closest('[draggable]') as HTMLElement;
    const unlinkedCol = within(board).getByLabelText('Coluna Sem conta vinculada no CRM');
    const dataTransfer = { getData: vi.fn().mockReturnValue(GROUP_ALPHA), setData: vi.fn() };
    fireEvent.dragStart(card, { dataTransfer });
    fireEvent.drop(unlinkedCol, { dataTransfer });
    await waitFor(() => expect(axios.delete).toHaveBeenCalledWith(expect.stringContaining('/tenants/A/groups/wa-alpha/account'), expect.anything()));
  });

  it('vincular: pede confirmação explícita e manda Idempotency-Key — nada dispara sem o clique final', async () => {
    renderAt(<HubGroupOrganizerPage />);
    const row = (await screen.findByText('Suporte geral')).closest('tr')!;
    await userEvent.click(within(row).getByRole('button', { name: 'Vincular a um Tenant' }));
    const dialog = screen.getByRole('dialog', { name: 'Vincular a um Tenant' });
    expect(vi.mocked(axios.post)).not.toHaveBeenCalled(); // abrir o modal não chama a API
    await expect(within(dialog).getByRole('button', { name: 'Vincular' })).toBeDisabled();
    await userEvent.selectOptions(within(dialog).getByLabelText('Tenant'), 'A');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Vincular' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalled());
    const [url, body, config] = vi.mocked(axios.post).mock.calls[0];
    expect(String(url)).toBe(`/api/v1/hubs/${HUB}/groups/catalog/${GROUP_UNCLASSIFIED}/links`);
    expect(body).toEqual({ tenant_id: 'A', role: 'consumer' });
    expect((config as { headers: Record<string, string> }).headers['Idempotency-Key']).toBeTruthy();
  });

  it('adicionar ao catálogo: escolhendo tenant, oferece as conexões e depois os grupos já conhecidos em vez de campos em branco', async () => {
    discoveredConnections = [{ id: '11111111-1111-1111-1111-111111111111', channel: 'whatsapp', provider: 'waha', external_number_id: '5511999990000', status: 'active' }];
    discovered = [{ provider_group_id: '551199990000001@g.us', name: 'Alpha Suporte' }, { provider_group_id: '551199990000002@g.us', name: 'Alpha Vendas' }];
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('button', { name: 'Adicionar grupo ao catálogo' }));
    const dialog = screen.getByRole('dialog', { name: 'Adicionar grupo ao catálogo' });
    expect(within(dialog).queryByLabelText('Conexão (canal) na origem')).not.toBeInTheDocument(); // sem tenant ainda, nada a descobrir
    expect(within(dialog).queryByLabelText('Grupo')).not.toBeInTheDocument();
    await userEvent.selectOptions(within(dialog).getByLabelText('Tenant de origem'), 'A');
    const connPicker = await within(dialog).findByLabelText('Conexão (canal) na origem');
    await userEvent.selectOptions(connPicker, '11111111-1111-1111-1111-111111111111');
    const picker = await within(dialog).findByLabelText('Grupo');
    await userEvent.selectOptions(picker, '551199990000002@g.us');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Adicionar' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalled());
    const [url, body] = vi.mocked(axios.post).mock.calls[0];
    expect(String(url)).toBe(`/api/v1/hubs/${HUB}/groups/catalog`);
    expect(body).toEqual({
      source_tenant_id: 'A',
      source_channel_connection_id: '11111111-1111-1111-1111-111111111111',
      provider_group_id: '551199990000002@g.us',
      display_name: 'Alpha Vendas', // preenchido sozinho pela seleção, não digitado
    });
  });

  it('adicionar ao catálogo: lista vazia de conexões cai pro campo manual em vez de travar', async () => {
    discoveredConnections = [];
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('button', { name: 'Adicionar grupo ao catálogo' }));
    const dialog = screen.getByRole('dialog', { name: 'Adicionar grupo ao catálogo' });
    await userEvent.selectOptions(within(dialog).getByLabelText('Tenant de origem'), 'A');
    await screen.findByText(/Nenhuma conexão de WhatsApp disponível/);
    await userEvent.click(within(dialog).getByRole('button', { name: 'Digitar manualmente' }));
    expect(within(dialog).getByLabelText('ID da conexão (canal) na origem')).toBeInTheDocument();
  });

  it('adicionar ao catálogo: lista vazia de grupos cai pro campo manual em vez de travar', async () => {
    discoveredConnections = [];
    discovered = [];
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('button', { name: 'Adicionar grupo ao catálogo' }));
    const dialog = screen.getByRole('dialog', { name: 'Adicionar grupo ao catálogo' });
    await userEvent.selectOptions(within(dialog).getByLabelText('Tenant de origem'), 'A');
    await screen.findByText(/Nenhuma conexão de WhatsApp disponível/);
    await userEvent.click(within(dialog).getByRole('button', { name: 'Digitar manualmente' }));
    await userEvent.type(within(dialog).getByLabelText('ID da conexão (canal) na origem'), '11111111-1111-1111-1111-111111111111');
    await screen.findByText(/Nenhum grupo disponível/);
    await userEvent.click(within(dialog).getByRole('button', { name: 'Digitar manualmente' }));
    expect(within(dialog).getByLabelText('Identificador do grupo (provider_group_id)')).toBeInTheDocument();
  });

  it('revogar vínculo: menu do vínculo abre confirmação, e só manda a versão esperada depois de confirmar', async () => {
    renderAt(<HubGroupOrganizerPage />);
    const row = (await screen.findByText('Clientes Alpha')).closest('tr')!;
    await userEvent.click(within(row).getByRole('button', { name: 'Ações do vínculo com ISP Roraima' }));
    await userEvent.click(screen.getByRole('menuitem', { name: 'Revogar este vínculo' }));
    const dialog = screen.getByRole('dialog', { name: 'Revogar vínculo' });
    expect(axios.post).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole('button', { name: 'Revogar' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalled());
    const [url, body] = vi.mocked(axios.post).mock.calls[0];
    expect(String(url)).toBe(`/api/v1/hubs/${HUB}/groups/catalog/${GROUP_ALPHA}/links/A/revoke`);
    expect(body).toEqual({ expected_version: 1 });
  });

  it('401 em qualquer chamada do organizador encaminha para a sessão expirada', async () => {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (String(url).endsWith('/hubs')) return { data: { items: hubs } };
      return Promise.reject({ response: { status: 401 } });
    });
    renderAt(<HubGroupOrganizerPage />);
    await waitFor(() => expect(unauthorized).toHaveBeenCalled());
  });
});
