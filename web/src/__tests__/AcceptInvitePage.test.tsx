import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import AcceptInvitePage from '../pages/AcceptInvitePage';
import { authAPI } from '../lib/api';
import { invitationAcceptAPI } from '../lib/invitations';
import { renderAt, setSession } from './testUtils';

// Mesmo problema de api.ts em Login.test.tsx: axios.create() quebra sob
// vi.mock('axios') puro, então mockamos os módulos que a tela consome.
vi.mock('../lib/api', () => ({
  authAPI: { startOIDC: vi.fn() },
}));
vi.mock('../lib/invitations', () => ({
  invitationAcceptAPI: { status: vi.fn(), accept: vi.fn() },
}));

const TOKEN = 'abcdEF0123456789xyz';
const page = () => renderAt(<AcceptInvitePage />, `/invite/${TOKEN}`, '/invite/:token');

describe('AcceptInvitePage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
  });

  it('shows SSO action when not authenticated', async () => {
    vi.mocked(invitationAcceptAPI.status).mockRejectedValue({ response: { status: 401 } });
    page();

    expect(await screen.findByRole('button', { name: 'Continuar com SSO' })).toBeInTheDocument();
  });

  it('starts SSO with the invite path as return_to', async () => {
    vi.mocked(invitationAcceptAPI.status).mockRejectedValue({ response: { status: 401 } });
    page();

    await userEvent.click(await screen.findByRole('button', { name: 'Continuar com SSO' }));
    expect(authAPI.startOIDC).toHaveBeenCalledWith(`/invite/${TOKEN}`);
  });

  it('shows the invite summary and accept action when authenticated', async () => {
    setSession();
    vi.mocked(invitationAcceptAPI.status).mockResolvedValue({
      status: 'pending', tenant_name: 'Empresa X', role_name: 'Agente', masked_email: 'n***@empresa.com',
    });
    page();

    expect(await screen.findByText(/Empresa X/)).toBeInTheDocument();
    expect(screen.getByText(/Agente/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Aceitar convite' })).toBeInTheDocument();
  });

  it('accepts the invitation and redirects', async () => {
    setSession();
    vi.mocked(invitationAcceptAPI.status).mockResolvedValue({
      status: 'pending', tenant_name: 'Empresa X', role_name: 'Agente',
    });
    vi.mocked(invitationAcceptAPI.accept).mockResolvedValue({ tenant_id: 't1' });
    page();

    await userEvent.click(await screen.findByRole('button', { name: 'Aceitar convite' }));

    expect(await screen.findByText(/Convite aceito/)).toBeInTheDocument();
    expect(invitationAcceptAPI.accept).toHaveBeenCalledWith(TOKEN);
  });

  it('shows a specific message for wrong identity', async () => {
    setSession();
    vi.mocked(invitationAcceptAPI.status).mockResolvedValue({ status: 'wrong_identity', masked_email: 'o***@empresa.com' });
    page();

    expect(await screen.findByText(/enviado para outro e-mail/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Aceitar convite' })).toBeNull();
  });

  it('shows expired state without an accept action', async () => {
    setSession();
    vi.mocked(invitationAcceptAPI.status).mockResolvedValue({ status: 'expired' });
    page();

    expect(await screen.findByText('Este convite expirou.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Aceitar convite' })).toBeNull();
  });

  it('shows revoked state', async () => {
    setSession();
    vi.mocked(invitationAcceptAPI.status).mockResolvedValue({ status: 'revoked' });
    page();

    expect(await screen.findByText('Este convite foi revogado.')).toBeInTheDocument();
  });

  it('shows not_found state for a bogus token', async () => {
    setSession();
    vi.mocked(invitationAcceptAPI.status).mockResolvedValue({ status: 'not_found' });
    page();

    expect(await screen.findByText('Convite não encontrado.')).toBeInTheDocument();
  });

  it('surfaces a conflict when accept races another acceptance', async () => {
    setSession();
    vi.mocked(invitationAcceptAPI.status).mockResolvedValue({ status: 'pending', tenant_name: 'Empresa X', role_name: 'Agente' });
    vi.mocked(invitationAcceptAPI.accept).mockRejectedValue({ response: { status: 409 } });
    page();

    await userEvent.click(await screen.findByRole('button', { name: 'Aceitar convite' }));
    await waitFor(() => expect(screen.getByText('Este convite já foi aceito.')).toBeInTheDocument());
  });
});
