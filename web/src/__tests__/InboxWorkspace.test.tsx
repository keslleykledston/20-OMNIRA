import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { useNavigate } from 'react-router-dom';
import axios from 'axios';
import userEvent from '@testing-library/user-event';
import InboxWorkspace from '../pages/InboxWorkspace';
import { renderAt, mockGets, setSession } from './testUtils';

// PRODUCT.6-O2D2: the frozen deep-link contract is /inbox?conversation_id=
// <uuid>. All HTTP is mocked — no live backend/K3G call is ever made from
// these tests. The SSE hook is stubbed exactly like ConversationPage.test.tsx
// already does, so no real fetch()-based stream is ever opened.
vi.mock('axios');
vi.mock('../hooks/useRealtimeEvents', () => ({
  useRealtimeEvents: () => {},
}));

// jsdom does not implement scrollIntoView; ChatPane calls it on every
// message-list update. Unrelated to this slice's own logic.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

const CONV_A = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
const CONV_B = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb';
const CONV_LIST_FIRST = 'cccccccc-cccc-cccc-cccc-cccccccccccc';

function conversation(id: string, over: object = {}) {
  return {
    id,
    contact_name: 'Alice',
    contact_phone: '+5511900000001',
    status: 'active',
    updated_at: new Date().toISOString(),
    message_count: 1,
    unread_count: 0,
    ...over,
  };
}

// Default list response used by every test unless overridden: one
// conversation, unrelated to the deep-link ids above, so a passing
// deep-link test can never be accidentally satisfied by the "auto-select
// first conversation" fallback instead of by real deep-link resolution.
function mockDefaultList() {
  return {
    '/inbox/conversations': { items: [conversation(CONV_LIST_FIRST)] },
    [`/inbox/conversations/${CONV_LIST_FIRST}`]: conversation(CONV_LIST_FIRST),
    [`/inbox/conversations/${CONV_LIST_FIRST}/messages`]: { items: [], has_more: false },
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  setSession();
});

describe('InboxWorkspace — PRODUCT.6-O2D2 conversation deep link', () => {
  // A
  it('/inbox without a query param keeps existing auto-select-first behavior unchanged', async () => {
    mockGets(mockDefaultList());
    renderAt(<InboxWorkspace />, '/inbox');
    // Auto-selected first conversation from the list — its own detail fetch resolves.
    await waitFor(() => {
      expect(
        vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith(`/inbox/conversations/${CONV_LIST_FIRST}`)),
      ).toBe(true);
    });
    expect(screen.queryByText('Selecione uma conversa')).not.toBeInTheDocument();
  });

  // B
  it('/inbox?conversation_id=<accessible> resolves and selects it, never the list-first fallback', async () => {
    mockGets({
      ...mockDefaultList(),
      [`/inbox/conversations/${CONV_A}`]: conversation(CONV_A, { contact_name: 'Deep Linked Contact' }),
      [`/inbox/conversations/${CONV_A}/messages`]: { items: [], has_more: false },
    });
    renderAt(<InboxWorkspace />, `/inbox?conversation_id=${CONV_A}`);

    await waitFor(() => {
      expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith(`/inbox/conversations/${CONV_A}`))).toBe(true);
    });
    // The list-first conversation must never have been fetched as a selection —
    // only the deep-linked one.
    expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith(`/inbox/conversations/${CONV_LIST_FIRST}/messages`))).toBe(
      false,
    );
  });

  // C
  it('a query change from A to B (no reload) selects B safely', async () => {
    mockGets({
      ...mockDefaultList(),
      [`/inbox/conversations/${CONV_A}`]: conversation(CONV_A),
      [`/inbox/conversations/${CONV_A}/messages`]: { items: [], has_more: false },
      [`/inbox/conversations/${CONV_B}`]: conversation(CONV_B),
      [`/inbox/conversations/${CONV_B}/messages`]: { items: [], has_more: false },
    });

    function Switcher() {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate(`/inbox?conversation_id=${CONV_B}`)}>switch to B</button>
          <InboxWorkspace />
        </div>
      );
    }

    const { fireEvent } = await import('@testing-library/react');
    renderAt(<Switcher />, `/inbox?conversation_id=${CONV_A}`);
    await waitFor(() => {
      expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith(`/inbox/conversations/${CONV_A}`))).toBe(true);
    });

    fireEvent.click(screen.getByRole('button', { name: 'switch to B' }));
    await waitFor(() => {
      expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith(`/inbox/conversations/${CONV_B}`))).toBe(true);
    });
  });

  // D
  it('an unknown conversation_id shows a safe not-found state, never a crash', async () => {
    mockGets(mockDefaultList()); // CONV_A intentionally not registered -> 404 fallback
    renderAt(<InboxWorkspace />, `/inbox?conversation_id=${CONV_A}`);
    expect(await screen.findByText('Conversa não encontrada ou sem acesso.')).toBeInTheDocument();
  });

  // E
  it('an inaccessible/cross-tenant conversation_id never leaks conversation data (identical 404 path)', async () => {
    // Mirrors the backend contract: GetConversation returns the SAME 404 for
    // "unknown" and "another tenant's conversation" — never distinguished.
    // The list-first conversation uses a DISTINCT name so a pass can never
    // be accidentally satisfied by that legitimate, unrelated sidebar entry.
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/inbox/conversations')) return { data: { items: [conversation(CONV_LIST_FIRST, { contact_name: 'Legitimate Contact' })] } };
      if (url.endsWith(`/inbox/conversations/${CONV_LIST_FIRST}`)) return { data: conversation(CONV_LIST_FIRST, { contact_name: 'Legitimate Contact' }) };
      if (url.endsWith(`/inbox/conversations/${CONV_LIST_FIRST}/messages`)) return { data: { items: [], has_more: false } };
      if (url.endsWith(`/inbox/conversations/${CONV_A}`)) {
        return Promise.reject({ response: { status: 404, data: 'conversation not found' } });
      }
      return Promise.reject({ response: { status: 404 } });
    });
    renderAt(<InboxWorkspace />, `/inbox?conversation_id=${CONV_A}`);
    await screen.findByText('Conversa não encontrada ou sem acesso.');
    expect(screen.queryByText('Alice')).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain(CONV_A);
  });

  // F
  it('a malformed conversation_id is ignored client-side — no request for it, ordinary auto-select proceeds', async () => {
    mockGets(mockDefaultList());
    renderAt(<InboxWorkspace />, '/inbox?conversation_id=not-a-uuid');

    await waitFor(() => {
      expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith(`/inbox/conversations/${CONV_LIST_FIRST}`))).toBe(true);
    });
    expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith('/inbox/conversations/not-a-uuid'))).toBe(false);
  });

  // G
  it('deep-link resolution goes through the authenticated endpoint (tenant/RLS authorization preserved)', async () => {
    mockGets({
      ...mockDefaultList(),
      [`/inbox/conversations/${CONV_A}`]: conversation(CONV_A),
      [`/inbox/conversations/${CONV_A}/messages`]: { items: [], has_more: false },
    });
    renderAt(<InboxWorkspace />, `/inbox?conversation_id=${CONV_A}`);

    await waitFor(() => {
      const call = vi.mocked(axios.get).mock.calls.find(([url]) => (url as string).endsWith(`/inbox/conversations/${CONV_A}`));
      expect(call).toBeDefined();
      const config = call?.[1] as any;
      expect(config?.headers?.Authorization).toBeDefined();
    });
  });

  // H
  it('a late response for a superseded deep link (A) never overwrites the newer selection (B)', async () => {
    let resolveA: (v: unknown) => void = () => {};
    vi.mocked(axios.get).mockImplementation((url: string) => {
      if (url.endsWith('/inbox/conversations')) return Promise.resolve({ data: { items: [conversation(CONV_LIST_FIRST)] } });
      if (url.endsWith(`/inbox/conversations/${CONV_LIST_FIRST}`)) return Promise.resolve({ data: conversation(CONV_LIST_FIRST) });
      if (url.endsWith(`/inbox/conversations/${CONV_LIST_FIRST}/messages`)) return Promise.resolve({ data: { items: [], has_more: false } });
      if (url.endsWith(`/inbox/conversations/${CONV_A}`)) return new Promise((resolve) => (resolveA = resolve));
      if (url.endsWith(`/inbox/conversations/${CONV_B}`)) return Promise.resolve({ data: conversation(CONV_B) });
      if (url.endsWith(`/inbox/conversations/${CONV_B}/messages`)) return Promise.resolve({ data: { items: [], has_more: false } });
      return Promise.reject({ response: { status: 404 } });
    });

    function Switcher() {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate(`/inbox?conversation_id=${CONV_B}`)}>switch to B</button>
          <InboxWorkspace />
        </div>
      );
    }

    const { fireEvent } = await import('@testing-library/react');
    renderAt(<Switcher />, `/inbox?conversation_id=${CONV_A}`);
    await screen.findByText('Abrindo conversa…');

    // Switch to B before A's resolution ever arrives.
    fireEvent.click(screen.getByRole('button', { name: 'switch to B' }));
    await waitFor(() => {
      expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith(`/inbox/conversations/${CONV_B}/messages`))).toBe(
        true,
      );
    });

    // A's late response must never re-select A over the newer B.
    resolveA({ data: conversation(CONV_A) });
    await waitFor(() => {});
    expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith(`/inbox/conversations/${CONV_A}/messages`))).toBe(
      false,
    );
  });

  // Section 10: default behavior preserved when no deep link is present.
  it('without conversation_id, filters/segment behavior is unaffected', async () => {
    mockGets(mockDefaultList());
    renderAt(<InboxWorkspace />, '/inbox');
    expect(await screen.findByRole('tab', { name: 'Todas' })).toBeInTheDocument();
    // "Não lidas" never filtered anything (there is no read tracking); "Aguardando" is the real one:
    // the customer wrote last and nobody answered.
    expect(screen.getByRole('tab', { name: 'Aguardando' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Minhas' })).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Não lidas' })).not.toBeInTheDocument();
  });
});

describe('InboxWorkspace — list paging, search and filters', () => {
  type Params = Record<string, unknown>;
  // Query params of every list request, in call order.
  const listCalls = (): Params[] =>
    vi
      .mocked(axios.get)
      .mock.calls.filter(([url]) => (url as string).endsWith('/inbox/conversations'))
      .map(([, config]) => ((config as { params?: Params } | undefined)?.params ?? {}));

  it('asks for the newest page first with no search/filter, and follows next_cursor while there is more', async () => {
    vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
      if ((url as string).endsWith('/inbox/conversations')) {
        return config?.params?.cursor
          ? { data: { items: [conversation('page-2', { contact_name: 'Antiga' })], has_more: false } }
          : { data: { items: [conversation('page-1', { contact_name: 'Recente' })], has_more: true, next_cursor: 'CUR1' } };
      }
      if ((url as string).includes('/messages')) return { data: { items: [], has_more: false } };
      return { data: conversation('page-1') };
    });
    renderAt(<InboxWorkspace />, '/inbox');
    // the short first page does not fill the panel, so the next page is requested on its own
    await waitFor(() => expect(screen.getByText('Antiga')).toBeInTheDocument());
    const [first, second] = listCalls();
    expect(first).toMatchObject({ limit: 100 });
    expect(first.cursor).toBeUndefined();
    expect(first.q).toBeUndefined();
    expect(first.assigned).toBeUndefined();
    expect(first.waiting).toBeUndefined();
    expect(second.cursor).toBe('CUR1');
    // newest first, exactly as the API returned it
    const names = screen.getAllByRole('listitem').map((li) => li.textContent);
    expect(names[0]).toContain('Recente');
    expect(names[1]).toContain('Antiga');
  });

  it('the Spam tab asks the API for kind=spam, and no other tab sends a kind', async () => {
    const user = userEvent.setup();
    mockGets(mockDefaultList());
    renderAt(<InboxWorkspace />, '/inbox');
    await screen.findByRole('tab', { name: 'Todas' });
    await user.selectOptions(screen.getByLabelText('Mais filtros'), 'spam');
    await waitFor(() => expect(listCalls().some((p) => p.kind === 'spam')).toBe(true));
    const others = listCalls().filter((p) => p.kind !== 'spam');
    expect(others.length).toBeGreaterThan(0);
    expect(others.every((p) => p.kind === undefined)).toBe(true);
    await user.click(screen.getByRole('tab', { name: 'Todas' }));
    await waitFor(() => expect(listCalls().filter((p) => p.kind === undefined).length).toBeGreaterThan(1));
  });

  it('the Não classif. and Internas tabs ask the API for that conversation_kind, and the other tabs send none (ADR-0018)', async () => {
    const user = userEvent.setup();
    mockGets(mockDefaultList());
    renderAt(<InboxWorkspace />, '/inbox');
    await screen.findByRole('tab', { name: 'Todas' });
    await user.selectOptions(screen.getByLabelText('Mais filtros'), 'unclassified');
    await waitFor(() => expect(listCalls().some((p) => p.conversation_kind === 'unclassified')).toBe(true));
    await user.selectOptions(screen.getByLabelText('Mais filtros'), 'internal');
    await waitFor(() => expect(listCalls().some((p) => p.conversation_kind === 'internal')).toBe(true));
    // the plain tabs never send it, so the server keeps staff conversations out of attendance
    expect(listCalls().filter((p) => p.conversation_kind === undefined).every((p) => p.kind === undefined || p.kind === 'spam')).toBe(true);
    await user.click(screen.getByRole('tab', { name: 'Todas' }));
    await waitFor(() => expect(listCalls().filter((p) => p.conversation_kind === undefined).length).toBeGreaterThan(1));
  });

  it('runs search and the Minhas / Aguardando filters on the server', async () => {
    const user = userEvent.setup();
    mockGets(mockDefaultList());
    renderAt(<InboxWorkspace />, '/inbox');
    await screen.findByRole('tab', { name: 'Todas' });

    await user.click(screen.getByRole('tab', { name: 'Minhas' }));
    await waitFor(() => expect(listCalls().some((p) => p.assigned === 'me')).toBe(true));
    await user.click(screen.getByRole('tab', { name: 'Aguardando' }));
    await waitFor(() => expect(listCalls().some((p) => p.waiting === true && p.assigned === undefined)).toBe(true));
    await user.click(screen.getByRole('tab', { name: 'Todas' }));

    await user.type(screen.getByRole('searchbox'), 'k3g');
    await waitFor(() => expect(listCalls().some((p) => p.q === 'k3g')).toBe(true));
    // the keystrokes are debounced: no request per letter
    expect(listCalls().filter((p) => p.q === 'k').length).toBe(0);
  });
});
