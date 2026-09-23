import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Login from '../pages/Login';
import { authAPI } from '../lib/api';

// FRONTEND.1 final landing check: "/" can render UnavailableSurface outside
// development (Dashboard is mock-backed), so it must never be the default
// post-login destination. Isolated from Login.test.tsx because it needs to
// spy on useNavigate rather than assert on rendered screens.
const navigateMock = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigateMock };
});

vi.mock('../lib/api', () => ({
  authAPI: { mode: vi.fn(), session: vi.fn(), devLogin: vi.fn(), startOIDC: vi.fn() },
}));

function mode(body: object) {
  vi.mocked(authAPI.mode).mockResolvedValue({ data: body } as any);
}

describe('Login — default landing destination', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    window.history.replaceState({}, '', '/login');
  });

  it('dev login with no explicit return destination lands on /inbox, not /', async () => {
    mode({ mode: 'dev', dev_auth: true });
    vi.mocked(authAPI.devLogin).mockResolvedValue({
      data: { token: 't', user: { id: 'u1', email: 'test@omnira.local' }, tenant: { id: 'tn1' } },
    } as any);
    render(
      <MemoryRouter initialEntries={['/login']}>
        <Login />
      </MemoryRouter>,
    );

    await screen.findByText('Modo de desenvolvimento');
    await userEvent.type(screen.getByLabelText('E-mail'), 'test@omnira.local');
    await userEvent.click(screen.getByRole('button', { name: 'Entrar como usuário de teste' }));

    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith('/inbox', { replace: true }));
  });

  it('OIDC completion with an active tenant lands on /inbox, not /', async () => {
    mode({ mode: 'oidc' });
    vi.mocked(authAPI.session).mockResolvedValue({
      data: { user: { id: 'u1', roles: [] }, tenant: { id: 'tn1' } },
    } as any);
    // Login.tsx reads window.location.search directly (not via the router).
    window.history.replaceState({}, '', '/login?oidc=complete');
    render(
      <MemoryRouter initialEntries={['/login?oidc=complete']}>
        <Login />
      </MemoryRouter>,
    );

    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith('/inbox', { replace: true }));
  });

  // Explicit safe return destinations (the /invite/:token allowlist) are
  // handled entirely by the backend's own HTTP redirect before the browser
  // ever reaches this component — this path only covers the true "no
  // explicit destination" default, which must never be /.
  it('OIDC completion without an active tenant still goes to /no-access (unchanged)', async () => {
    mode({ mode: 'oidc' });
    vi.mocked(authAPI.session).mockResolvedValue({
      data: { user: { id: 'u1', roles: [] }, tenant: undefined },
    } as any);
    window.history.replaceState({}, '', '/login?oidc=complete');
    render(
      <MemoryRouter initialEntries={['/login?oidc=complete']}>
        <Login />
      </MemoryRouter>,
    );

    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith('/no-access', { replace: true }));
  });
});
