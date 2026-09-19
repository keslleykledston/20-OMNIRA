import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import ChannelsPage from '../pages/ChannelsPage';
import { renderAt, setSession, TENANT } from './testUtils';

vi.mock('axios');

const BASE = `/api/v1/tenants/${TENANT}/channels/waha/connections`;
const conn = (over: object = {}) => ({
  id: 'aaaaaaaa-1111-2222-3333-444444444444', provider: 'waha', provider_kind: 'unofficial', status: 'pending',
  capabilities: ['text'], created_at: new Date().toISOString(), ...over,
});
const page = () => renderAt(<ChannelsPage />, '/channels', '/channels');

// Answers GETs by URL; anything else is a 404.
function gets(routes: Record<string, () => unknown | Promise<unknown>>) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    const key = Object.keys(routes).find((k) => url === k);
    if (!key) return Promise.reject({ response: { status: 404, data: 'nope' } });
    const out = await routes[key]();
    return { data: out };
  });
}

describe('ChannelsPage', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.clear();
    setSession();
  });

  it('lists connections with the JWT and shows their status', async () => {
    gets({ [BASE]: () => ({ items: [conn({ status: 'active', external_account_id: '5511988887777' })] }) });
    page();
    expect(await screen.findByText('aaaaaaaa')).toBeInTheDocument();
    expect(screen.getByTestId('conn-status')).toHaveTextContent('active');
    expect(screen.getByText(/Connected as \+5511988887777/)).toBeInTheDocument();
    expect((vi.mocked(axios.get).mock.calls[0] as any[])[1].headers.Authorization).toBe('Bearer tok');
  });

  it('requires the risk acknowledgement before creating, then posts it', async () => {
    let items: any[] = [];
    gets({ [BASE]: () => ({ items }) });
    vi.mocked(axios.post).mockImplementation(async () => {
      items = [conn()];
      return { data: conn() };
    });
    page();
    const create = await screen.findByRole('button', { name: 'Create connection' });
    expect(create).toBeDisabled();
    await userEvent.click(screen.getByRole('checkbox'));
    expect(create).toBeEnabled();
    await userEvent.click(create);
    expect(await screen.findByText('aaaaaaaa')).toBeInTheDocument(); // list refreshed
    const [url, body, config] = vi.mocked(axios.post).mock.calls[0] as any[];
    expect(url).toBe(BASE);
    expect(body).toEqual({ risk_acknowledged: true });
    expect(config.headers.Authorization).toBe('Bearer tok');
  });

  it('shows a permission message (no form) when the backend answers 403', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 403, data: 'forbidden' } });
    page();
    expect(await screen.findByRole('alert')).toHaveTextContent('Only tenant administrators');
    expect(screen.queryByRole('button', { name: 'Create connection' })).toBeNull();
  });

  it('starts the session, shows the QR code and then the connected account', async () => {
    const id = conn().id;
    let live: any = conn({ session_status: 'needs_qr' });
    gets({
      [BASE]: () => ({ items: [conn()] }),
      [`${BASE}/${id}`]: () => live,
      [`${BASE}/${id}/qr`]: () => ({ mimetype: 'image/png', data: 'QRBASE64' }),
    });
    vi.mocked(axios.post).mockResolvedValue({ data: live });
    page();
    await userEvent.click(await screen.findByRole('button', { name: 'Start session' }));
    const img = await screen.findByAltText('WhatsApp pairing QR code');
    expect(img).toHaveAttribute('src', 'data:image/png;base64,QRBASE64');
    expect(vi.mocked(axios.post).mock.calls[0][0]).toBe(`${BASE}/${id}/session/start`);
    // The operator scans: the next poll reports a working session.
    live = conn({ status: 'active', session_status: 'working', external_account_id: '5511988887777' });
    await waitFor(() => expect(screen.getByText(/Connected as \+5511988887777/)).toBeInTheDocument(), { timeout: 4000 });
    expect(screen.queryByAltText('WhatsApp pairing QR code')).toBeNull();
    expect(screen.getByTestId('conn-status')).toHaveTextContent('active');
  });

  it('maps gateway/configuration errors to actionable messages', async () => {
    const id = conn().id;
    gets({ [BASE]: () => ({ items: [conn()] }) });
    vi.mocked(axios.post).mockRejectedValueOnce({ response: { status: 503, data: 'x' } });
    page();
    await userEvent.click(await screen.findByRole('button', { name: 'Start session' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('not configured on the server');
    vi.mocked(axios.post).mockRejectedValueOnce({ response: { status: 502, data: 'x' } });
    await userEvent.click(screen.getByRole('button', { name: 'Start session' }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('WAHA'));
    expect(id).toBeTruthy();
  });

  it('stops the session', async () => {
    const id = conn().id;
    gets({ [BASE]: () => ({ items: [conn({ status: 'active' })] }) });
    vi.mocked(axios.post).mockResolvedValue({ data: conn({ status: 'disconnected', session_status: 'stopped' }) });
    page();
    await userEvent.click(await screen.findByRole('button', { name: 'Stop' }));
    await waitFor(() => expect(screen.getByTestId('conn-status')).toHaveTextContent('disconnected'));
    expect(vi.mocked(axios.post).mock.calls[0][0]).toBe(`${BASE}/${id}/session/stop`);
  });
});
