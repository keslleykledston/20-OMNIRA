import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import { RolesPermissionsPage } from '../pages/RolesPermissionsPage';
import { renderAt, mockGets, setSession } from './testUtils';

vi.mock('axios');

const roles = [
  { id: 'r3', key: 'tenant_agent', name: 'Tenant Agent', permissions: ['conversation.claim', 'tenant.read'] },
  {
    id: 'r1', key: 'tenant_admin', name: 'Tenant Administrator',
    permissions: ['audit.read', 'channel.manage', 'conversation.claim', 'conversation.manage',
      'membership.manage', 'membership.read', 'tenant.manage', 'tenant.read'],
  },
  {
    id: 'r2', key: 'tenant_supervisor', name: 'Tenant Supervisor',
    permissions: ['audit.read', 'conversation.claim', 'conversation.manage', 'membership.read', 'tenant.read'],
  },
];
const myAccess = (role_key: string) => ({ role_key, permissions: [], invitation_delivery_available: false });

const page = () => renderAt(<RolesPermissionsPage />, '/settings/roles', '/settings/roles');
const row = (label: string) => screen.getByText(label).closest('li') as HTMLElement;

describe('RolesPermissionsPage (read-only)', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.clear();
    setSession();
  });

  it('lists the three fixed roles in order and opens on the caller role', async () => {
    mockGets({ '/roles': { items: roles }, '/me/access': myAccess('tenant_supervisor') });
    page();
    const tabs = await screen.findAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual(['Administrador', 'Supervisor', 'Agente']);
    expect(screen.getByRole('tab', { name: 'Supervisor' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('Seu papel')).toBeInTheDocument();
    expect(screen.getByText('As permissões deste papel são definidas pela plataforma.')).toBeInTheDocument();
  });

  it('shows the effective matrix of the selected role', async () => {
    mockGets({ '/roles': { items: roles }, '/me/access': myAccess('tenant_agent') });
    page();
    await screen.findAllByRole('tab');
    // agent: claim + tenant.read only
    expect(within(row('Assumir e responder conversas')).getByText('Permitido')).toBeInTheDocument();
    expect(within(row('Gerenciar e transferir conversas')).getByText('Não permitido')).toBeInTheDocument();
    expect(within(row('Gerenciar canais')).getByText('Não permitido')).toBeInTheDocument();

    await userEvent.click(screen.getByRole('tab', { name: 'Administrador' }));
    expect(within(row('Gerenciar canais')).getByText('Permitido')).toBeInTheDocument();
    expect(within(row('Gerenciar equipe')).getByText('Permitido')).toBeInTheDocument();
    // the badge follows the caller's own role, not the selected tab
    expect(screen.queryByText('Seu papel')).toBeNull();
  });

  it('has no editable controls at all', async () => {
    mockGets({ '/roles': { items: roles }, '/me/access': myAccess('tenant_admin') });
    page();
    await screen.findAllByRole('tab');
    for (const role of ['checkbox', 'switch', 'textbox', 'combobox', 'radio']) {
      expect(screen.queryAllByRole(role)).toHaveLength(0);
    }
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('surfaces a granted permission that has no label instead of hiding it', async () => {
    const extra = [{ ...roles[0], permissions: ['conversation.claim', 'future.thing'] }, roles[1], roles[2]];
    mockGets({ '/roles': { items: extra }, '/me/access': myAccess('tenant_agent') });
    page();
    await screen.findAllByRole('tab');
    expect(screen.getByText('Outras')).toBeInTheDocument();
    expect(screen.getByText('future.thing')).toBeInTheDocument();
  });

  it('explains a 403 instead of rendering an empty matrix', async () => {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/roles')) return Promise.reject({ response: { status: 403, data: 'forbidden' } });
      return { data: myAccess('tenant_agent') };
    });
    page();
    expect(await screen.findByText('Você não tem permissão para ver as funções e permissões.')).toBeInTheDocument();
    expect(screen.queryAllByRole('tab')).toHaveLength(0);
  });
});
