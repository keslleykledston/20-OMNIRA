import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import ChatPane from '../components/inbox/ChatPane';
import { renderAt, renderAtWithClient, setSession } from './testUtils';
import type { MessageItem } from '../types/api';

// The realtime hook is stubbed but its callback is kept, so a test can fire "something changed".
const realtime = vi.hoisted(() => ({ onEvent: undefined as undefined | (() => void) }));
vi.mock('axios');
vi.mock('../hooks/useRealtimeEvents', () => ({
  useRealtimeEvents: (opts: { onEvent?: () => void }) => {
    realtime.onEvent = opts.onEvent;
  },
}));

const CONV = 'conv-1';
const T = (h: number, m = 0) => new Date(2026, 9, 3, h, m).toISOString();
const msg = (id: string, h: number, over: Partial<MessageItem> = {}): MessageItem => ({
  id,
  conversation_id: CONV,
  body: `texto ${id}`,
  direction: 'inbound',
  status: 'received',
  created_at: T(h),
  ...over,
});

// Backend: newest-first pages of the whole thread, cursor = how many were already served.
let thread: MessageItem[] = [];
let assignedTo: string | undefined = 'u-1';
let pageSize = 100;
let channelData: Record<string, unknown> | undefined;
let events: Record<string, unknown>[] = [];
let timelineDown = false;
function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    if (channelData && (url as string).endsWith(`/inbox/conversations/${CONV}/channel`)) return { data: channelData };
    if ((url as string).endsWith(`/inbox/conversations/${CONV}`)) {
      return { data: { id: CONV, contact_name: 'Maria', contact_phone: '+5511999990000', status: 'active', assigned_to_user_id: assignedTo } };
    }
    if ((url as string).endsWith('/messages')) {
      const newestFirst = [...thread].sort((a, b) => b.created_at.localeCompare(a.created_at));
      const offset = Number(config?.params?.cursor || 0);
      const items = newestFirst.slice(offset, offset + pageSize);
      const more = offset + pageSize < newestFirst.length;
      return { data: { items, has_more: more, next_cursor: more ? String(offset + pageSize) : undefined } };
    }
    if ((url as string).endsWith(`/inbox/conversations/${CONV}/timeline`)) return timelineDown ? Promise.reject({ response: { status: 500 } }) : { data: { items: events } };
    return Promise.reject({ response: { status: 404 } });
  });
}

const originals = {
  scrollHeight: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollHeight'),
  clientHeight: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientHeight'),
};
beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  setSession();
  pageSize = 100;
  assignedTo = 'u-1';
  channelData = undefined;
  events = [];
  timelineDown = false;
  Object.defineProperty(HTMLElement.prototype, 'scrollHeight', { configurable: true, get: () => 1000 });
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => 400 });
});
afterEach(() => {
  for (const [k, d] of Object.entries(originals)) {
    if (d) Object.defineProperty(HTMLElement.prototype, k, d);
    else delete (HTMLElement.prototype as any)[k];
  }
});

const scroller = () => document.querySelector('.overflow-y-auto') as HTMLElement;
const order = () => screen.getAllByText(/^texto /).map((n) => n.textContent);

describe('ChatPane — who spoke', () => {
  it('labels automation apart from the team, and names a new run when the sender kind changes', async () => {
    thread = [
      msg('bot', 10, { direction: 'outbound', sender_kind: 'automation' }),
      msg('pessoa', 11, { direction: 'outbound', sender_kind: 'agent' }),
    ];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto pessoa');
    expect(screen.getByText('Automação')).toBeInTheDocument();
    expect(screen.getByText('Equipe')).toBeInTheDocument();
  });
});

describe('ChatPane — what happened to the attendance', () => {
  it('draws the events as lines between the messages, in time order, and a line ends the run of the speaker before it', async () => {
    thread = [msg('a', 10, { direction: 'outbound', sender_kind: 'agent' }), msg('b', 12, { direction: 'outbound', sender_kind: 'agent' })];
    events = [
      { id: 'assignment:1', kind: 'claimed', at: T(11), actor_id: 'u-1' },
      { id: 'protocol_opened:1', kind: 'protocol_opened', at: T(11, 5), protocol: 'KTG-1007' },
    ];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto b');
    const claimed = await screen.findByText('Você assumiu o atendimento');
    const protocol = screen.getByText('Protocolo KTG-1007 aberto');
    const a = screen.getByText('texto a');
    const b = screen.getByText('texto b');
    const before = (x: Node, y: Node) => !!(x.compareDocumentPosition(y) & Node.DOCUMENT_POSITION_FOLLOWING);
    expect(before(a, claimed)).toBe(true);
    expect(before(claimed, protocol)).toBe(true);
    expect(before(protocol, b)).toBe(true);
    // the same speaker, but an event came between: the second bubble names the speaker again
    expect(screen.getAllByText('Equipe')).toHaveLength(2);
  });

  it('still shows the messages when the events cannot be read', async () => {
    thread = [msg('a', 10)];
    events = [{ id: 'assignment:1', kind: 'claimed', at: T(11), actor_id: 'u-1' }];
    timelineDown = true;
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto a');
    expect(screen.queryByText(/assumiu o atendimento/)).not.toBeInTheDocument();
  });
});

describe('ChatPane — thread order and scrolling', () => {
  it('shows the oldest message at the top and the newest at the bottom', async () => {
    thread = [msg('c', 12), msg('a', 10), msg('b', 11)]; // API returns them newest first
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto a');
    expect(order()).toEqual(['texto a', 'texto b', 'texto c']);
  });

  it('anchors a short thread to the bottom, next to the composer', async () => {
    thread = [msg('a', 10)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto a');
    expect(document.querySelector('.justify-end')).not.toBeNull();
  });

  it('opens scrolled to the last message', async () => {
    thread = [msg('a', 10), msg('b', 11)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto b');
    expect(scroller().scrollTop).toBe(1000);
  });

  it('follows a message you send: back to the bottom even if you had scrolled up', async () => {
    const user = userEvent.setup();
    thread = [msg('a', 10), msg('b', 11)];
    serve();
    vi.mocked(axios.post).mockImplementation(async () => {
      thread = [...thread, msg('mine', 12, { direction: 'outbound', status: 'queued', body: 'texto mine' })];
      return { data: {} };
    });
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto b');
    const el = scroller();
    el.scrollTop = 0; // reading old messages
    fireEvent.scroll(el);
    await user.type(screen.getByPlaceholderText('Escreva uma resposta...'), 'oi{Enter}');
    await screen.findByText('texto mine');
    await waitFor(() => expect(el.scrollTop).toBe(1000));
  });

  it('follows a received message while you are at the bottom, but does not yank you down while you read older ones', async () => {
    thread = [msg('a', 10), msg('b', 11)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto b');
    const el = scroller();
    expect(el.scrollTop).toBe(1000);

    // at the bottom: a received message is followed
    el.scrollTop = 900; // 1000 - 900 - 400 < 160: still "at the bottom"
    fireEvent.scroll(el);
    thread = [...thread, msg('c', 12)];
    await act(async () => realtime.onEvent?.());
    await screen.findByText('texto c');
    await waitFor(() => expect(el.scrollTop).toBe(1000));

    // reading older messages: a new one arrives, the view stays and the jump button shows
    el.scrollTop = 100; // 800 above the bottom
    fireEvent.scroll(el);
    thread = [...thread, msg('d', 13)];
    await act(async () => realtime.onEvent?.());
    await screen.findByText('texto d');
    expect(el.scrollTop).toBe(100);
    expect(screen.getByLabelText('Ir para a última mensagem')).toBeInTheDocument();
  });

  it('the jump button goes to the last message and then disappears', async () => {
    const user = userEvent.setup();
    thread = [msg('a', 10), msg('b', 11)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto b');
    const el = scroller();
    el.scrollTop = 0;
    fireEvent.scroll(el);
    await user.click(await screen.findByLabelText('Ir para a última mensagem'));
    expect(el.scrollTop).toBe(1000);
    expect(screen.queryByLabelText('Ir para a última mensagem')).not.toBeInTheDocument();
  });
});

describe('ChatPane — older messages', () => {
  it('loads older pages on scroll to the top and keeps them above, in order, without duplicates', async () => {
    pageSize = 2;
    thread = [msg('1', 8), msg('2', 9), msg('3', 10), msg('4', 11), msg('5', 12)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto 5');
    expect(order()).toEqual(['texto 4', 'texto 5']);
    const el = scroller();
    el.scrollTop = 0;
    fireEvent.scroll(el);
    await screen.findByText('texto 3');
    await waitFor(() => expect(order()).toEqual(['texto 2', 'texto 3', 'texto 4', 'texto 5']));
    fireEvent.scroll(el);
    await screen.findByText('texto 1');
    expect(order()).toEqual(['texto 1', 'texto 2', 'texto 3', 'texto 4', 'texto 5']);
    expect(screen.queryByText('Carregar mensagens anteriores')).not.toBeInTheDocument();
  });

  it('offers a button to load older messages while there are more', async () => {
    const user = userEvent.setup();
    pageSize = 2;
    thread = [msg('1', 8), msg('2', 9), msg('3', 10)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await user.click(await screen.findByText('Carregar mensagens anteriores'));
    await screen.findByText('texto 1');
  });
});

describe('ChatPane — day separators', () => {
  it('uses "Hoje"/"Ontem" and does not repeat a day within the same day', async () => {
    const now = new Date();
    const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 10).toISOString();
    const today1 = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 5).toISOString();
    const today2 = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 6).toISOString();
    thread = [
      msg('y', 10, { created_at: yesterday }),
      msg('t1', 10, { created_at: today1 }),
      msg('t2', 10, { created_at: today2 }),
    ];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto t2');
    expect(screen.getAllByText('Hoje')).toHaveLength(1);
    expect(screen.getAllByText('Ontem')).toHaveLength(1);
  });
});

describe('ChatPane — spam contact (ADR-0014)', () => {
  function serveKind(kind: string) {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if ((url as string).endsWith(`/inbox/conversations/${CONV}`)) {
        return { data: { id: CONV, contact_name: 'Golpe', contact_phone: '+5511999990000', status: 'active', contact_kind: kind, assigned_to_user_id: 'u-1' } };
      }
      if ((url as string).endsWith('/messages')) return { data: { items: [msg('a', 10)], has_more: false } };
      return Promise.reject({ response: { status: 404 } });
    });
  }

  it('switches replying off for a spam contact and says how to restore', async () => {
    serveKind('spam');
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto a');
    expect(await screen.findByText(/respostas ficam desativadas/)).toBeInTheDocument();
    expect(screen.queryByPlaceholderText('Escreva uma resposta...')).not.toBeInTheDocument();
  });

  it('keeps the composer for any other contact', async () => {
    serveKind('customer');
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto a');
    expect(await screen.findByPlaceholderText('Escreva uma resposta...')).toBeInTheDocument();
    expect(screen.queryByText(/respostas ficam desativadas/)).not.toBeInTheDocument();
  });
});

describe('ChatPane — claim first (channel selector slice)', () => {
  it('without an owner it asks to take the attendance instead of offering a composer that would be refused', async () => {
    assignedTo = undefined;
    thread = [msg('a', 9)];
    serve();
    vi.mocked(axios.post).mockResolvedValue({ data: {} });
    renderAt(<ChatPane conversationId={CONV} />);
    expect(await screen.findByText('Assuma o atendimento para responder.')).toBeInTheDocument();
    expect(screen.queryByPlaceholderText('Escreva uma resposta...')).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Assumir' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.post).mock.calls[0][0])).toMatch(/inbox\/conversations\/conv-1\/assign$/);
  });
  it('after assuming, the protocol panel reloads (the claim may have opened it): a cached "no protocol" must not stay on screen', async () => {
    assignedTo = undefined;
    thread = [msg('a', 9)];
    serve();
    vi.mocked(axios.post).mockResolvedValue({ data: {} });
    const { queryClient } = renderAtWithClient(<ChatPane conversationId={CONV} />);
    queryClient.setQueryData(['service-session', 'member', 'tenant-1', CONV], null);
    await screen.findByText('Assuma o atendimento para responder.');
    await userEvent.click(screen.getByRole('button', { name: 'Assumir' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(queryClient.getQueryState(['service-session', 'member', 'tenant-1', CONV])?.isInvalidated).toBe(true));
  });
  it('says why the claim was refused when the person is not an agent of this instance (422), instead of doing nothing', async () => {
    assignedTo = undefined;
    thread = [msg('a', 9)];
    serve();
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 422, data: 'assignee is not an eligible agent of this tenant\n' } });
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('Assuma o atendimento para responder.');
    await userEvent.click(screen.getByRole('button', { name: 'Assumir' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(/não é atendente desta instância/i);
  });
});

describe('ChatPane — the two actions of the status strip are real buttons (UX audit A1)', () => {
  it('"Assumir atendimento" and "Finalizar" are bordered or filled buttons with an icon, not bare links', async () => {
    assignedTo = undefined;
    thread = [msg('a', 9)];
    serve();
    const first = renderAt(<ChatPane conversationId={CONV} />);
    const claimBtn = await screen.findByRole('button', { name: 'Assumir atendimento' });
    expect(claimBtn.className).toMatch(/bg-accent-primary/);
    expect(claimBtn.querySelector('svg')).not.toBeNull();
    first.unmount();

    assignedTo = 'u-1';
    renderAt(<ChatPane conversationId={CONV} />);
    const finalize = await screen.findByRole('button', { name: 'Finalizar' });
    expect(finalize.className).toMatch(/border-border-strong/);
    expect(finalize.querySelector('svg')).not.toBeNull();
  });
});

describe('ChatPane — the 24 h window says how long is left (UX audit M6)', () => {
  const channelState = (expiresInMs: number) => ({ can_send_text: true, can_send_media: false, window_required: true, window_open: true, window_expires_at: new Date(Date.now() + expiresInMs).toISOString() });
  it('is calm with hours left and turns into a warning with the template shortcut under two hours', async () => {
    thread = [msg('a', 9)];
    channelData = channelState(5 * 3_600_000 + 12 * 60_000);
    serve();
    const calm = renderAt(<ChatPane conversationId={CONV} />);
    const calmNote = await screen.findByText(/restam 5 h/);
    expect(calmNote.closest('[role="status"]')?.className).not.toMatch(/bg-status-warning-soft/);
    calm.unmount();

    channelData = channelState(40 * 60_000);
    renderAt(<ChatPane conversationId={CONV} />);
    const urgentNote = await screen.findByText(/restam (39|40) min/);
    expect(urgentNote.closest('[role="status"]')?.className).toMatch(/bg-status-warning-soft/);
    expect(screen.getByRole('button', { name: 'Enviar template' })).toBeInTheDocument();
  });

  it('asks the server again the moment the window runs out, so free text is not offered after it closed', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      thread = [msg('a', 9)];
      channelData = channelState(60_000);
      serve();
      renderAt(<ChatPane conversationId={CONV} />);
      await screen.findByText(/resta/);
      const channelCalls = () => vi.mocked(axios.get).mock.calls.filter(([u]) => String(u).endsWith(`/inbox/conversations/${CONV}/channel`)).length;
      const before = channelCalls();
      channelData = { ...channelState(0), window_open: false, window_expires_at: new Date(Date.now() - 1000).toISOString() };
      await act(async () => { vi.advanceTimersByTime(61_000); });
      await waitFor(() => expect(channelCalls()).toBeGreaterThan(before));
      expect(await screen.findByText(/Janela de 24 h fechada/)).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });
});
