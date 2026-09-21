import { describe, it, expect, beforeEach, vi } from 'vitest';
import axios from 'axios';
import { authHeaders, getTenantId, saveSession, clearSession, isUnauthorized } from '../lib/session';
import { authAPI } from '../lib/api';

describe('session', () => {
  beforeEach(() => localStorage.clear());

  it('stores and clears token, tenant and user', () => {
    saveSession('jwt', 'tenant-1', { id: 'u1' });
    expect(authHeaders()).toEqual({ Authorization: 'Bearer jwt' });
    expect(getTenantId()).toBe('tenant-1');
    clearSession();
    expect(authHeaders()).toEqual({});
    expect(getTenantId()).toBe('');
    expect(localStorage.getItem('user')).toBeNull();
  });

  it('detects 401 responses', () => {
    expect(isUnauthorized({ response: { status: 401 } })).toBe(true);
    expect(isUnauthorized({ response: { status: 403 } })).toBe(false);
    expect(isUnauthorized(new Error('x'))).toBe(false);
  });
});

describe('authAPI.devLogin', () => {
  it('posts only the email to the dev route and returns token, user and tenant', async () => {
    const spy = vi.spyOn(axios.Axios.prototype, 'request').mockResolvedValue({
      data: { token: 'jwt', user: { id: 'u1', email: 'a@b', name: 'A' }, tenant: { id: 't1', name: 'T' } },
    } as any);
    const res = await authAPI.devLogin('a@b');
    expect(res.data.token).toBe('jwt');
    expect(res.data.tenant.id).toBe('t1');
    expect(res.data.user.roles).toEqual([]);
    const cfg = spy.mock.calls[0][0] as any;
    // Caminho próprio: o acesso de desenvolvimento não se passa por login normal.
    expect(cfg.url ?? cfg).toContain('/v1/auth/dev/login');
    const sent = typeof cfg.data === 'string' ? JSON.parse(cfg.data) : cfg.data;
    // Sem senha: não existe senha a enviar.
    expect(sent).toEqual({ email: 'a@b' });
    spy.mockRestore();
  });
});
