import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Login from '../pages/Login';
import { authAPI } from '../lib/api';
import { renderAt } from './testUtils';

// O alvo aqui é a tela: o que ela oferece em cada modo que o servidor anuncia.
// O transporte do dev login está coberto em session.test.ts.
vi.mock('../lib/api', () => ({
  authAPI: {
    mode: vi.fn(),
    session: vi.fn(),
    devLogin: vi.fn(),
    startOIDC: vi.fn(),
  },
}));

const page = () => renderAt(<Login />, '/login', '/login');

function mode(body: object) {
  vi.mocked(authAPI.mode).mockResolvedValue({ data: body } as any);
}

describe('Login — produção (OIDC)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    window.history.replaceState({}, '', '/login');
  });

  // Não existe autenticação local por senha; oferecer o campo seria prometer
  // uma proteção que nada implementa.
  it('não mostra campo de senha', async () => {
    mode({ mode: 'oidc' });
    const { container } = page();

    await screen.findByRole('button', { name: 'Continuar com SSO' });
    expect(container.querySelector('input[type="password"]')).toBeNull();
    expect(screen.queryByText(/esqueci minha senha/i)).toBeNull();
  });

  it('não mostra formulário de e-mail e senha', async () => {
    mode({ mode: 'oidc' });
    const { container } = page();

    await screen.findByRole('button', { name: 'Continuar com SSO' });
    expect(container.querySelectorAll('input')).toHaveLength(0);
  });

  it('oferece SSO como ação', async () => {
    mode({ mode: 'oidc' });
    page();

    const sso = await screen.findByRole('button', { name: 'Continuar com SSO' });
    expect(sso).toBeEnabled();
  });

  it('não mostra o bloco de desenvolvimento', async () => {
    mode({ mode: 'oidc', dev_auth: false });
    page();

    await screen.findByRole('button', { name: 'Continuar com SSO' });
    expect(screen.queryByText('Modo de desenvolvimento')).toBeNull();
  });
});

describe('Login — modo de desenvolvimento', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    window.history.replaceState({}, '', '/login');
  });

  it('mostra o bloco rotulado, com e-mail e sem senha', async () => {
    mode({ mode: 'dev', dev_auth: true });
    const { container } = page();

    expect(await screen.findByText('Modo de desenvolvimento')).toBeInTheDocument();
    expect(screen.getByLabelText('E-mail')).toBeInTheDocument();
    expect(container.querySelector('input[type="password"]')).toBeNull();
  });

  it('envia ao endpoint de desenvolvimento, só com e-mail', async () => {
    mode({ mode: 'dev', dev_auth: true });
    vi.mocked(authAPI.devLogin).mockResolvedValue({
      data: { token: 't', user: { id: 'u1', email: 'test@omnira.local' }, tenant: { id: 'tn1' } },
    } as any);
    page();

    await screen.findByText('Modo de desenvolvimento');
    await userEvent.type(screen.getByLabelText('E-mail'), 'test@omnira.local');
    await userEvent.click(screen.getByRole('button', { name: 'Entrar como usuário de teste' }));

    await waitFor(() => expect(authAPI.devLogin).toHaveBeenCalledWith('test@omnira.local'));
  });

  // A rota some quando o servidor não a registra; a tela precisa dizer isso em
  // vez de insinuar que o e-mail estava errado.
  it('explica quando a rota não existe no servidor', async () => {
    mode({ mode: 'dev', dev_auth: true });
    vi.mocked(authAPI.devLogin).mockRejectedValue({ response: { status: 404 } });
    page();

    await screen.findByText('Modo de desenvolvimento');
    await userEvent.type(screen.getByLabelText('E-mail'), 'x@y.local');
    await userEvent.click(screen.getByRole('button', { name: 'Entrar como usuário de teste' }));

    expect(
      await screen.findByText('O acesso de desenvolvimento não está disponível neste servidor.'),
    ).toBeInTheDocument();
  });
});

describe('Login — estados', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    window.history.replaceState({}, '', '/login');
  });

  it('erro de OIDC não expõe a mensagem crua do provedor', async () => {
    mode({ mode: 'oidc' });
    vi.mocked(authAPI.session).mockRejectedValue({
      response: { status: 500, data: 'invalid_grant: PKCE verifier mismatch' },
    });
    window.history.replaceState({}, '', '/login?oidc=complete');
    page();

    expect(
      await screen.findByText('Não foi possível concluir o login. Tente novamente ou contate o administrador.'),
    ).toBeInTheDocument();
    expect(screen.queryByText(/PKCE/)).toBeNull();
  });

  it('sessão expirada é informativa, não erro', async () => {
    mode({ mode: 'oidc' });
    window.history.replaceState({}, '', '/login?reason=session_expired');
    page();

    const notice = await screen.findByRole('status');
    expect(notice).toHaveTextContent('Sua sessão expirou. Entre novamente para continuar.');
  });

  it('avisa quando nenhum método está configurado', async () => {
    mode({ mode: 'unavailable', dev_auth: false });
    page();

    expect(
      await screen.findByText('Nenhum método de autenticação está configurado neste ambiente.'),
    ).toBeInTheDocument();
  });
});

describe('Login — teclado', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    window.history.replaceState({}, '', '/login');
  });

  it('a ação de SSO é alcançável por Tab', async () => {
    mode({ mode: 'oidc' });
    page();

    const sso = await screen.findByRole('button', { name: 'Continuar com SSO' });
    await userEvent.tab();
    expect(sso).toHaveFocus();
  });

  it('Enter no campo de e-mail submete o acesso de desenvolvimento', async () => {
    mode({ mode: 'dev', dev_auth: true });
    vi.mocked(authAPI.devLogin).mockResolvedValue({
      data: { token: 't', user: { id: 'u1' }, tenant: { id: 'tn1' } },
    } as any);
    page();

    await screen.findByText('Modo de desenvolvimento');
    await userEvent.type(screen.getByLabelText('E-mail'), 'test@omnira.local{Enter}');

    await waitFor(() => expect(authAPI.devLogin).toHaveBeenCalled());
  });
});
