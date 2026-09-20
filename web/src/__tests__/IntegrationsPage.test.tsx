import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import IntegrationsPage from '../pages/IntegrationsPage';
import { renderAt, setSession, TENANT } from './testUtils';

vi.mock('axios');

const CHANNELS = `/api/v1/tenants/${TENANT}/channels`;
const CONNECTIONS = `${CHANNELS}/connections`;
const PROVIDERS = `${CHANNELS}/providers`;
const waha = {
  id: 'waha', name: 'WhatsApp (não oficial)', channel: 'whatsapp', kind: 'unofficial', connect_method: 'qr_session',
  risk_notice: 'O número pode ser banido.', capabilities: ['text', 'qr_pairing'], enabled: true, inputs: [], displays: [],
};
const meta = {
  id: 'meta_cloud', name: 'WhatsApp · Meta Cloud', channel: 'whatsapp', kind: 'official', connect_method: 'credentials',
  capabilities: ['text'], enabled: false, unavailable_reason: 'Disponível na I2.', inputs: [], displays: [],
};
const conn = (over: object = {}) => ({
  id: 'aaaaaaaa-1111-2222-3333-444444444444', provider: 'waha', provider_kind: 'unofficial', status: 'pending',
  capabilities: ['text'], created_at: new Date().toISOString(), ...over,
});
const page = () => renderAt(<IntegrationsPage />, '/integrations', '/integrations');

function gets(routes: Record<string, () => unknown | Promise<unknown>>) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    const route = routes[url];
    if (!route) return Promise.reject({ response: { status: 404, data: 'nope' } });
    return { data: await route() };
  });
}

describe('IntegrationsPage', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.clear();
    setSession();
  });

  it('renders connected cards using provider descriptors', async () => {
    gets({
      [PROVIDERS]: () => ({ items: [meta, waha] }),
      [CONNECTIONS]: () => ({ items: [conn({ status: 'active', external_account_id: '5511988887777' })] }),
    });
    page();
    expect(await screen.findByText('WhatsApp (não oficial)')).toBeInTheDocument();
    expect(screen.getByTestId('conn-status')).toHaveTextContent('Conectado');
    expect(screen.getByText(/Conectado como \+5511988887777/)).toBeInTheDocument();
    expect((vi.mocked(axios.get).mock.calls[0] as any[])[1].headers.Authorization).toBe('Bearer tok');
  });

  it('drives provider selection and risk acceptance from the descriptor', async () => {
    let items: any[] = [];
    gets({ [PROVIDERS]: () => ({ items: [meta, waha] }), [CONNECTIONS]: () => ({ items }) });
    vi.mocked(axios.post).mockImplementation(async () => {
      items = [conn()];
      return { data: conn() };
    });
    page();
    await userEvent.click(await screen.findByRole('button', { name: '+ Adicionar integração' }));
    expect(screen.getByRole('button', { name: /WhatsApp · Meta Cloud/ })).toBeDisabled();
    expect(screen.getByText('Disponível na I2.')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /WhatsApp \(não oficial\)/ }));
    expect(screen.getByRole('alert')).toHaveTextContent('O número pode ser banido');
    const create = screen.getByRole('button', { name: 'Criar conexão' });
    expect(create).toBeDisabled();
    await userEvent.click(screen.getByRole('checkbox'));
    await userEvent.click(create);
    expect(await screen.findByText(/Conexão criada/)).toBeInTheDocument();
    const [url, body, config] = vi.mocked(axios.post).mock.calls[0] as any[];
    expect(url).toBe(CONNECTIONS);
    expect(body).toEqual({ provider: 'waha', inputs: {}, risk_acknowledged: true });
    expect(config.headers.Authorization).toBe('Bearer tok');
  });

  it('shows permission denial and hides the add action', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 403, data: 'forbidden' } });
    page();
    expect(await screen.findByRole('alert')).toHaveTextContent('Somente administradores');
    expect(screen.queryByRole('button', { name: '+ Adicionar integração' })).toBeNull();
  });

  it('starts pairing, renders the QR in a modal and observes the connected account', async () => {
    const id = conn().id;
    // Começa sem sessão para o botão inicial ser estável: o polling do card só
    // passa a oferecer "Ver QR" depois que o servidor reportar needs_qr.
    let live: any = conn();
    gets({
      [PROVIDERS]: () => ({ items: [waha] }), [CONNECTIONS]: () => ({ items: [conn()] }),
      [`${CONNECTIONS}/${id}`]: () => live,
      [`${CONNECTIONS}/${id}/qr`]: () => ({ mimetype: 'image/png', data: 'QRBASE64' }),
    });
    vi.mocked(axios.post).mockImplementation(async () => {
      live = conn({ session_status: 'needs_qr' });
      return { data: live } as any;
    });
    page();
    await userEvent.click(await screen.findByRole('button', { name: 'Iniciar sessão' }));
    const modal = await screen.findByRole('dialog', { name: /Conectar/ });
    expect(await screen.findByTestId('qr-image')).toHaveAttribute('src', 'data:image/png;base64,QRBASE64');
    expect(within(modal).getByTestId('qr-modal-status')).toHaveTextContent('Aguardando leitura do QR');
    expect(vi.mocked(axios.post).mock.calls[0][0]).toBe(`${CONNECTIONS}/${id}/session/start`);
    live = conn({ status: 'active', session_status: 'working', external_account_id: '5511988887777' });
    await waitFor(() => expect(screen.getByTestId('qr-connected')).toBeInTheDocument(), { timeout: 4000 });
    expect(screen.queryByTestId('qr-image')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'Concluir' }));
    expect(screen.queryByRole('dialog', { name: /Conectar/ })).toBeNull();
    expect(screen.getByText(/Conectado como \+5511988887777/)).toBeInTheDocument();
  });

  it('reflects the live status without opening the modal', async () => {
    const id = conn().id;
    let live: any = conn({ status: 'pending', session_status: 'starting' });
    gets({
      [PROVIDERS]: () => ({ items: [waha] }), [CONNECTIONS]: () => ({ items: [conn({ status: 'pending' })] }),
      [`${CONNECTIONS}/${id}`]: () => live,
    });
    page();
    // Sem nenhum clique: o card sozinho passa a refletir o estado do servidor.
    await waitFor(() => expect(screen.getByTestId('conn-status')).toHaveTextContent('Pendente'));
    live = conn({ status: 'active', session_status: 'working', external_account_id: '5511988887777' });
    await waitFor(() => expect(screen.getByTestId('conn-status')).toHaveTextContent('Conectado'), { timeout: 6000 });
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('maps provider errors and stops a session', async () => {
    const id = conn().id;
    gets({ [PROVIDERS]: () => ({ items: [waha] }), [CONNECTIONS]: () => ({ items: [conn({ status: 'active' })] }) });
    vi.mocked(axios.post).mockRejectedValueOnce({ response: { status: 502 } });
    page();
    await userEvent.click(await screen.findByRole('button', { name: 'Parar' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('gateway do WhatsApp');
    vi.mocked(axios.post).mockResolvedValueOnce({ data: conn({ status: 'disconnected', session_status: 'stopped' }) });
    await userEvent.click(screen.getByRole('button', { name: 'Parar' }));
    await waitFor(() => expect(screen.getByTestId('conn-status')).toHaveTextContent('Desconectado'));
    expect(vi.mocked(axios.post).mock.calls[1][0]).toBe(`${CONNECTIONS}/${id}/session/stop`);
  });
});
