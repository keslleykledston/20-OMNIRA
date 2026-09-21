import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import TeamPage from '../pages/TeamPage';
import { renderAt, setSession, TENANT } from './testUtils';

vi.mock('axios');

const BASE = `/api/v1/tenants/${TENANT}`;

const member = (over: object = {}) => ({
  membership_id: 'm-' + Math.random().toString(36).slice(2),
  user_id: 'u-' + Math.random().toString(36).slice(2),
  name: 'Ana Souza',
  email: 'ana@empresa.com',
  role_key: 'tenant_agent',
  role_name: 'Tenant Agent',
  status: 'active',
  created_at: '2026-01-10T12:00:00Z',
  ...over,
});

const ROLES = [
  { id: 'r1', key: 'tenant_admin', name: 'Administrador' },
  { id: 'r2', key: 'tenant_supervisor', name: 'Supervisor' },
  { id: 'r3', key: 'tenant_agent', name: 'Agente' },
];

function mockRoutes(routes: Record<string, () => unknown>) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    const key = Object.keys(routes).find((k) => url.endsWith(k));
    if (!key) return Promise.reject({ response: { status: 404 } });
    return { data: routes[key]() };
  });
}

const page = () => renderAt(<TeamPage />, '/settings/team', '/settings/team');

describe('TeamPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    setSession();
  });

  it('shows the "no access" state when the actor has no membership.read', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 404 } });
    page();

    expect(await screen.findByText('Você não tem permissão para gerenciar a equipe.')).toBeInTheDocument();
  });

  it('lists members and summary counts', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({
        items: [
          member({ name: 'Ana Souza', role_key: 'tenant_admin' }),
          member({ name: 'Bruno Lima', role_key: 'tenant_agent' }),
        ],
      }),
      '/roles': () => ({ items: ROLES }),
    });
    page();

    expect((await screen.findAllByText('Ana Souza')).length).toBeGreaterThan(0);
    expect(screen.getAllByText('Bruno Lima').length).toBeGreaterThan(0);
  });

  it('hides row actions for a supervisor without membership.manage', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_supervisor', permissions: ['membership.read'] }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: [] }),
    });
    page();

    await screen.findAllByText('Ana Souza');
    expect(screen.queryByRole('button', { name: 'Ações' })).toBeNull();
  });

  it('the invite button is disabled with an explanatory title when lacking permission', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_supervisor', permissions: ['membership.read'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: [] }),
    });
    page();

    const invite = await screen.findByRole('button', { name: /Convidar usuário/ });
    expect(invite).toBeDisabled();
    expect(invite).toHaveAttribute('title', 'Requer permissão para gerenciar a equipe');
  });

  it('the invite button is disabled with a delivery-specific title when unavailable', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: false }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: ROLES }),
    });
    page();

    // O botão existe desde o primeiro render, com o título do estado de
    // carregamento ("sem permissão" por padrão); espera a equipe carregar —
    // o que implica access.data já resolvido — antes de checar o título final.
    await screen.findAllByText('Ana Souza');
    const invite = screen.getByRole('button', { name: /Convidar usuário/ });
    expect(invite).toBeDisabled();
    await waitFor(() =>
      expect(invite).toHaveAttribute('title', 'Envio de convites ainda não está configurado neste ambiente.'),
    );
  });

  it('filters by search text across name and email', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({
        items: [member({ name: 'Ana Souza', email: 'ana@empresa.com' }), member({ name: 'Bruno Lima', email: 'bruno@empresa.com' })],
      }),
      '/roles': () => ({ items: ROLES }),
    });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.type(screen.getByPlaceholderText('Buscar por nome ou e-mail…'), 'bruno');

    expect(screen.queryAllByText('Ana Souza').length).toBe(0);
    expect(screen.getAllByText('Bruno Lima').length).toBeGreaterThan(0);
  });

  it('shows — when a member never logged in', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member({ last_login_at: undefined })] }),
      '/roles': () => ({ items: ROLES }),
    });
    page();

    await screen.findAllByText('Ana Souza');
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
  });

  it('changes a member role through the modal', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member({ membership_id: 'm1', role_key: 'tenant_agent' })] }),
      '/roles': () => ({ items: ROLES }),
    });
    vi.mocked(axios.patch).mockResolvedValue({ data: { membership_id: 'm1', role_key: 'tenant_supervisor' } });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.click((await screen.findAllByRole('button', { name: 'Ações' }))[0]);
    await userEvent.click(screen.getByRole('menuitem', { name: 'Alterar função' }));

    const dialog = await screen.findByRole('dialog');
    await userEvent.click(within(dialog).getByLabelText('Supervisor'));
    await userEvent.click(within(dialog).getByRole('button', { name: 'Salvar' }));

    await waitFor(() =>
      expect(axios.patch).toHaveBeenCalledWith(
        expect.stringContaining(`${BASE}/team/m1`),
        { role_key: 'tenant_supervisor' },
        expect.anything(),
      ),
    );
  });

  it('confirms before revoking a member', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member({ membership_id: 'm1' })] }),
      '/roles': () => ({ items: ROLES }),
    });
    vi.mocked(axios.patch).mockResolvedValue({ data: { membership_id: 'm1', status: 'revoked' } });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.click((await screen.findAllByRole('button', { name: 'Ações' }))[0]);
    await userEvent.click(screen.getByRole('menuitem', { name: 'Remover do tenant' }));

    expect(await screen.findByText(/perde acesso permanentemente/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Remover do tenant' }));

    await waitFor(() =>
      expect(axios.patch).toHaveBeenCalledWith(
        expect.stringContaining(`${BASE}/team/m1`),
        { status: 'revoked' },
        expect.anything(),
      ),
    );
  });

  it('surfaces the last-admin conflict from the backend', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member({ membership_id: 'm1', role_key: 'tenant_admin' })] }),
      '/roles': () => ({ items: ROLES }),
    });
    vi.mocked(axios.patch).mockRejectedValue({ response: { status: 409 } });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.click((await screen.findAllByRole('button', { name: 'Ações' }))[0]);
    await userEvent.click(screen.getByRole('menuitem', { name: 'Desativar acesso' }));
    await userEvent.click(screen.getByRole('button', { name: 'Desativar acesso' }));

    expect(
      await screen.findByText('O tenant ficaria sem nenhum administrador ativo.'),
    ).toBeInTheDocument();
  });

  it('empty team shows the empty state', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [] }),
      '/roles': () => ({ items: ROLES }),
    });
    page();

    expect(await screen.findByText('Nenhum membro encontrado.')).toBeInTheDocument();
  });
});

describe('TeamPage — convites', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    setSession();
  });

  const invite = (over: object = {}) => ({
    id: 'inv-' + Math.random().toString(36).slice(2),
    email: 'novo@empresa.com',
    role_key: 'tenant_agent',
    role_name: 'Tenant Agent',
    status: 'pending',
    created_by_email: 'admin@empresa.com',
    expires_at: new Date(Date.now() + 71 * 60 * 60 * 1000).toISOString(),
    created_at: new Date().toISOString(),
    ...over,
  });

  it('opens the invite modal with role options and submits', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: ROLES }),
      '/team/invitations': () => ({ items: [] }),
    });
    vi.mocked(axios.post).mockResolvedValue({ data: invite() });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.click(screen.getByRole('button', { name: /Convidar usuário/ }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Administrador')).toBeInTheDocument();
    expect(within(dialog).getByText('Supervisor')).toBeInTheDocument();
    expect(within(dialog).getByText('Agente')).toBeInTheDocument();

    await userEvent.type(within(dialog).getByLabelText('E-mail'), 'novo@empresa.com');
    await userEvent.selectOptions(within(dialog).getByLabelText('Função'), 'tenant_agent');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Enviar convite' }));

    await waitFor(() =>
      expect(axios.post).toHaveBeenCalledWith(
        expect.stringContaining('/team/invitations'),
        { email: 'novo@empresa.com', role_key: 'tenant_agent' },
        expect.anything(),
      ),
    );
  });

  it('validates the email before submitting', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: ROLES }),
      '/team/invitations': () => ({ items: [] }),
    });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.click(screen.getByRole('button', { name: /Convidar usuário/ }));
    const dialog = await screen.findByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Enviar convite' }));

    expect(within(dialog).getByText('Informe um e-mail válido.')).toBeInTheDocument();
    expect(axios.post).not.toHaveBeenCalled();
  });

  it('the invite button is hidden/disabled without membership.manage', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_supervisor', permissions: ['membership.read'] }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: [] }),
      '/team/invitations': () => ({ items: [] }),
    });
    page();

    await screen.findAllByText('Ana Souza');
    expect(screen.getByRole('button', { name: /Convidar usuário/ })).toBeDisabled();
  });

  it('lists pending invitations in the Convites tab', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: ROLES }),
      '/team/invitations': () => ({ items: [invite({ email: 'pendente@empresa.com' })] }),
    });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.click(screen.getByRole('tab', { name: 'Convites' }));

    expect((await screen.findAllByText('pendente@empresa.com')).length).toBeGreaterThan(0);
    expect(screen.getAllByText('Pendente').length).toBeGreaterThan(0);
  });

  it('revokes a pending invitation after confirmation', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: ROLES }),
      '/team/invitations': () => ({ items: [invite({ id: 'inv-1', email: 'a-revogar@empresa.com' })] }),
    });
    vi.mocked(axios.patch).mockResolvedValue({ data: {} });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.click(screen.getByRole('tab', { name: 'Convites' }));
    await screen.findAllByText('a-revogar@empresa.com');

    await userEvent.click((await screen.findAllByRole('button', { name: 'Ações' }))[0]);
    await userEvent.click(screen.getByRole('menuitem', { name: 'Revogar convite' }));

    const confirmDialog = screen.getByRole('dialog');
    await userEvent.click(within(confirmDialog).getByRole('button', { name: 'Revogar convite' }));

    await waitFor(() =>
      expect(axios.patch).toHaveBeenCalledWith(
        expect.stringContaining('/team/invitations/inv-1'),
        { status: 'revoked' },
        expect.anything(),
      ),
    );
  });

  it('shows an empty state when there are no invitations', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: ROLES }),
      '/team/invitations': () => ({ items: [] }),
    });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.click(screen.getByRole('tab', { name: 'Convites' }));

    expect(await screen.findByText('Nenhum convite enviado ainda.')).toBeInTheDocument();
  });

  it('does not offer revoke for an already-accepted invitation', async () => {
    mockRoutes({
      '/me/access': () => ({ role_key: 'tenant_admin', permissions: ['membership.read', 'membership.manage'], invitation_delivery_available: true }),
      '/team': () => ({ items: [member()] }),
      '/roles': () => ({ items: ROLES }),
      '/team/invitations': () => ({ items: [invite({ status: 'accepted', email: 'aceito@empresa.com' })] }),
    });
    page();

    await screen.findAllByText('Ana Souza');
    await userEvent.click(screen.getByRole('tab', { name: 'Convites' }));
    await screen.findAllByText('aceito@empresa.com');

    expect(screen.queryAllByRole('button', { name: 'Ações' }).length).toBe(0);
  });
});
