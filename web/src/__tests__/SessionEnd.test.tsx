import { beforeEach, describe, expect, it } from 'vitest';
import { clearSession, onSessionEnd, saveSession } from '../lib/session';
import { queryClient } from '../lib/queryClient';
import { authAPI } from '../lib/api';

// What one person loaded or typed must not be there for the next person who signs in in the same tab (the keys of the queries and of the drafts do not carry the
// person). Pre-gate review (Codex), 2026-10-12.
describe('ending a session forgets what the person loaded and typed', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    queryClient.clear();
  });

  it('empties the query cache on sign-out and when a new session starts', () => {
    queryClient.setQueryData(['hub-inbox', 'hub-1'], { items: [{ customer_name: 'Cliente da Alice' }] });
    expect(queryClient.getQueryData(['hub-inbox', 'hub-1'])).toBeDefined();
    clearSession();
    expect(queryClient.getQueryData(['hub-inbox', 'hub-1'])).toBeUndefined();
    queryClient.setQueryData(['my-hubs'], [{ id: 'hub-1' }]);
    saveSession('', 'tenant-1', { id: 'bob' });
    expect(queryClient.getQueryData(['my-hubs'])).toBeUndefined();
  });

  it('drops the tickets being created and the unsent drafts kept for conversations', () => {
    localStorage.setItem('omnira.ticket-create.conv-1', '{"idempotencyKey":"k"}');
    localStorage.setItem('omnira.ticket-status.conv-1', '{}');
    localStorage.setItem('omnira.sidebar.collapsed', '1'); // a per-viewer convenience of the BROWSER stays
    let ran = 0;
    onSessionEnd(() => {
      ran += 1;
    });
    clearSession();
    expect(localStorage.getItem('omnira.ticket-create.conv-1')).toBeNull();
    expect(localStorage.getItem('omnira.ticket-status.conv-1')).toBeNull();
    expect(localStorage.getItem('omnira.sidebar.collapsed')).toBe('1');
    expect(ran).toBe(1);
  });

  it('the logout call of the API layer (used by the no-access page too) empties the cache as well', async () => {
    queryClient.setQueryData(['my-tenants'], [{ id: 't1' }]);
    await authAPI.logout();
    expect(queryClient.getQueryData(['my-tenants'])).toBeUndefined();
  });
});

