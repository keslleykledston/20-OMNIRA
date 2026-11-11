import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
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

  it('pastas: deriva a pasta compartilhada SÓ da relação comercial + vínculos ativos dos dois lados, nunca manual', async () => {
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: /^Pastas/ }));
    expect(await screen.findByRole('heading', { name: 'ISP Roraima ↔ NorteNet' })).toBeInTheDocument();
    const shared = screen.getByLabelText('Pasta ISP Roraima ↔ NorteNet');
    expect(within(shared).getByText('Trânsito compartilhado')).toBeInTheDocument();
    // "Clientes Alpha" só está vinculado a A, não aparece na compartilhada
    expect(within(shared).queryByText('Clientes Alpha')).toBeNull();
  });

  it('kanban sem drag-and-drop: mover é só pelo menu "Vincular…", nunca um gesto de arrastar', async () => {
    renderAt(<HubGroupOrganizerPage />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Kanban' }));
    const board = await screen.findByRole('list', { name: 'Quadro de grupos por pasta' });
    expect(within(board).getByLabelText('Coluna Sem classificação')).toBeInTheDocument();
    expect(within(board).getByLabelText('Coluna ISP Roraima')).toBeInTheDocument();
    // nenhum elemento é arrastável (draggable), em nenhuma visão
    expect(document.querySelectorAll('[draggable="true"]').length).toBe(0);
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
