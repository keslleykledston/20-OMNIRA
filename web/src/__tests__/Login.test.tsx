import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Login from '../pages/Login';
import { authAPI } from '../lib/api';
import { renderAt } from './testUtils';
import { rememberAfterLogin } from '../lib/afterLogin';

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

vi.mock('../lib/tenants', () => ({ resolveSessionTenant: vi.fn(async (id?: string) => id ?? 'tenant-a-uuid') }));

const page = () => renderAt(<Login />, '/login', '/login');

function mode(body: object) {
  vi.mocked(authAPI.mode).mockResolvedValue({ data: body } as any);
}

// What the server says about the session cookie: signed in, or refused with a status (undefined = the request itself failed).
const signedIn = () =>
  vi.mocked(authAPI.session).mockResolvedValue({ data: { user: { id: 'u', email: 'a@b.c', name: 'A' }, tenant: { id: 'tenant-a-uuid' } } } as any);
const sessionRefused = (status?: number) =>
  vi.mocked(authAPI.session).mockRejectedValue(status === undefined ? new Error('network') : Object.assign(new Error('http'), { response: { status } }));

function reset(url = '/login') {
  vi.clearAllMocks();
  vi.mocked(authAPI.session).mockReset();
  localStorage.clear();
  sessionStorage.clear();
  window.history.replaceState({}, '', url);
}

// The page that stays on screen: someone who has just signed out is not taken straight back in.
const STAY = '/login?reason=signed_out';

describe('Login — produção (OIDC)', () => {
  beforeEach(() => reset(STAY));

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

  it('o botão leva ao login do provedor', async () => {
    mode({ mode: 'oidc' });
    page();

    await userEvent.click(await screen.findByRole('button', { name: 'Continuar com SSO' }));
    expect(authAPI.startOIDC).toHaveBeenCalledTimes(1);
  });

  it('não mostra o bloco de desenvolvimento', async () => {
    mode({ mode: 'oidc', dev_auth: false });
    page();

    await screen.findByRole('button', { name: 'Continuar com SSO' });
    expect(screen.queryByText('Modo de desenvolvimento')).toBeNull();
  });
});

// A tela de entrada não é um passo a mais: com sessão cai direto no trabalho, sem sessão vai direto ao login do provedor.
describe('Login — entrada automática (OIDC)', () => {
  beforeEach(() => reset());

  it('sem sessão: vai direto ao login do provedor, sem pedir clique', async () => {
    mode({ mode: 'oidc' });
    sessionRefused(401);
    page();

    await waitFor(() => expect(authAPI.startOIDC).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole('button', { name: 'Continuar com SSO' })).toBeNull();
    expect(screen.getByText('Indo para o login…')).toBeInTheDocument();
  });

  it('com sessão: entra direto na tela de trabalho, sem mostrar a tela de entrada', async () => {
    mode({ mode: 'oidc' });
    signedIn();
    page();

    await screen.findByTestId('elsewhere');
    expect(authAPI.startOIDC).not.toHaveBeenCalled();
    expect(localStorage.getItem('sessionActive')).toBe('true');
    expect(localStorage.getItem('tenantId')).toBe('tenant-a-uuid');
  });

  it('acabou de sair: não refaz a entrada sozinha e avisa', async () => {
    reset(STAY);
    mode({ mode: 'oidc' });
    page();

    expect(await screen.findByRole('button', { name: 'Continuar com SSO' })).toBeEnabled();
    expect(screen.getByRole('status')).toHaveTextContent('Você saiu do OMNIRA.');
    expect(authAPI.session).not.toHaveBeenCalled();
    expect(authAPI.startOIDC).not.toHaveBeenCalled();
  });

  it('foi ao provedor há instantes e voltou sem entrar: mostra o botão em vez de repetir (vaivém)', async () => {
    mode({ mode: 'oidc' });
    sessionRefused(401);
    sessionStorage.setItem('omnira.sso-auto', String(Date.now() - 2_000));
    page();

    expect(await screen.findByRole('button', { name: 'Continuar com SSO' })).toBeEnabled();
    expect(authAPI.startOIDC).not.toHaveBeenCalled();
  });

  it('a tentativa antiga não bloqueia: passados 30 s volta a seguir sozinha', async () => {
    mode({ mode: 'oidc' });
    sessionRefused(401);
    sessionStorage.setItem('omnira.sso-auto', String(Date.now() - 31_000));
    page();

    await waitFor(() => expect(authAPI.startOIDC).toHaveBeenCalledTimes(1));
  });

  it('entrar com sucesso apaga a marca da tentativa (a próxima saída não cai no vaivém)', async () => {
    mode({ mode: 'oidc' });
    signedIn();
    sessionStorage.setItem('omnira.sso-auto', String(Date.now()));
    page();

    await screen.findByTestId('elsewhere');
    expect(sessionStorage.getItem('omnira.sso-auto')).toBeNull();
  });

  it('conta sem acesso a nenhuma organização vai para /no-access', async () => {
    mode({ mode: 'oidc' });
    sessionRefused(403);
    page();

    await screen.findByTestId('elsewhere');
    expect(authAPI.startOIDC).not.toHaveBeenCalled();
  });

  it('não conseguir consultar a sessão (rede/servidor) não manda ao provedor nem acusa erro: oferece o botão', async () => {
    mode({ mode: 'oidc' });
    sessionRefused(undefined);
    page();

    expect(await screen.findByRole('button', { name: 'Continuar com SSO' })).toBeEnabled();
    expect(authAPI.startOIDC).not.toHaveBeenCalled();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('voltando do provedor sem sessão: mostra o erro e NÃO volta ao provedor (sem laço)', async () => {
    mode({ mode: 'oidc' });
    sessionRefused(401);
    window.history.replaceState({}, '', '/login?oidc=complete');
    page();

    expect(
      await screen.findByText('Não foi possível concluir o login. Tente novamente ou contate o administrador.'),
    ).toBeInTheDocument();
    expect(authAPI.startOIDC).not.toHaveBeenCalled();
  });

  it('modo de desenvolvimento e sem método configurado não consultam a sessão nem redirecionam', async () => {
    mode({ mode: 'dev', dev_auth: true });
    page();
    await screen.findByText('Modo de desenvolvimento');
    expect(authAPI.session).not.toHaveBeenCalled();
    expect(authAPI.startOIDC).not.toHaveBeenCalled();
  });
});

describe('Login — modo de desenvolvimento', () => {
  beforeEach(() => reset());

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
  beforeEach(() => reset());

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

  // Em produção a sessão expirada já segue sozinha ao provedor; o aviso existe para quando a tela fica (aqui, sem SSO).
  it('sessão expirada é informativa, não erro', async () => {
    mode({ mode: 'dev' });
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
  beforeEach(() => reset(STAY));

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

describe('Login — retorno ao console (ADR-0041)', () => {
  const realLocation = window.location;
  const replace = vi.fn();
  beforeEach(() => {
    reset('/login?oidc=complete');
    Object.defineProperty(window, 'location', { configurable: true, value: { ...realLocation, search: '?oidc=complete', pathname: '/login', replace } });
  });
  afterEach(() => {
    Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
  });

  it('volta ao endereço do console que mandou a pessoa entrar', async () => {
    mode({ mode: 'oidc' });
    signedIn();
    rememberAfterLogin('/admin/acessos?aba=equipes');
    page();
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/admin/acessos?aba=equipes'));
  });

  it('sem endereço lembrado segue o destino padrão (nada de /admin/)', async () => {
    mode({ mode: 'oidc' });
    signedIn();
    page();
    await screen.findByTestId('elsewhere');
    expect(replace).not.toHaveBeenCalled();
  });
});
